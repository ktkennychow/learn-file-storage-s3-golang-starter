package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

const (
	AspectRatio16_9 float64 = 16.0 / 9.0
	AspectRatio9_16 float64 = 9.0 / 16.0
)

const (
	Landscape = "16:9"
	Portrait  = "9:16"
	Other     = "other"
)

func processVideoForFastStart(filePath string) (string, error) {
	fastStartFilePath := filePath + ".processing"
	result := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", fastStartFilePath)
	err := result.Run()
	if err != nil {
		return "", err
	}

	return fastStartFilePath, nil
}

func getVideoAspectRatio(filePath string) (string, error) {
	result := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	buf := bytes.Buffer{}
	result.Stdout = &buf
	err := result.Run()
	if err != nil {
		return "", err
	}

	type FFProbeResult struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	var probeResult FFProbeResult
	err = json.Unmarshal(buf.Bytes(), &probeResult)
	if err != nil {
		return "", err
	}

	if len(probeResult.Streams) == 0 {
		return "", errors.New("unable to process video")
	}

	width := probeResult.Streams[0].Width
	height := probeResult.Streams[0].Height

	var aspectRatio float64 = float64(width) / float64(height)
	const tolerance = 0.01

	switch {
	case math.Abs(aspectRatio-AspectRatio16_9) <= tolerance:
		return Landscape, nil
	case math.Abs(aspectRatio-AspectRatio9_16) <= tolerance:
		return Portrait, nil
	default:
		return Other, nil
	}
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)
	presignedHttpReq, err := presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{Bucket: &bucket, Key: &key}, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}
	return presignedHttpReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}

	splitUrl := strings.Split(*video.VideoURL, ",")
	presignedUrl, err := generatePresignedURL(cfg.s3Client, splitUrl[0], splitUrl[1], time.Minute*60)
	if err != nil {
		return database.Video{}, err
	}
	video.VideoURL = &presignedUrl
	return video, nil
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading video", videoID, "by user", userID)

	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't access video file or header", err)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Problem identifying mediaType", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusUnauthorized, "Media type not allowed", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem creating temprary file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	io.Copy(tempFile, file)
	tempFile.Seek(0, io.SeekStart)

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem identifying aspect ratio", err)
		return
	}

	fastStartTempFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem creating fast start version", err)
		return
	}

	fastStartTempFile, err := os.Open(fastStartTempFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem opening fast start version", err)
		return
	}

	randBytes := make([]byte, 32)
	_, err = rand.Read(randBytes)
	randString := base64.RawURLEncoding.EncodeToString(randBytes)

	var orientation string

	switch aspectRatio {
	case "16:9":
		orientation = "landscape"
	case "9:16":
		orientation = "portrait"
	default:
		orientation = "other"
	}

	filename := fmt.Sprintf("%v/%v.%v", orientation, randString, path.Base(contentType))

	_, err = cfg.s3Client.PutObject(context.Background(), &s3.PutObjectInput{Bucket: &cfg.s3Bucket, Key: &filename, Body: fastStartTempFile, ContentType: &mediaType})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem uploading video to s3", err)
		return
	}

	s3VideoURL := fmt.Sprintf("%v,%v", cfg.s3Bucket, filename)

	video.VideoURL = &s3VideoURL

	cfg.db.UpdateVideo(video)

	video, err = cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem presigning video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
