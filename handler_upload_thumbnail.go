package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// TODO: implement the upload here
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", err)
		return
	}

	const maxMemory = 10 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse multipart form", err)
		return
	}

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't access thumbnail file or header", err)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")

  mediaType, _, err := mime.ParseMediaType(contentType)
  if err != nil {
    respondWithError(w, http.StatusUnauthorized, "Problem identifying mediaType", err)
    return
  }
  if mediaType != "image/jpeg" && mediaType != "image/png" {
    respondWithError(w, http.StatusUnauthorized, "Media type not allowed", err)
    return
  }
  
	randBytes := make([]byte, 32)
	_, err = rand.Read(randBytes)
	randString := base64.RawURLEncoding.EncodeToString(randBytes)

	filename := fmt.Sprintf("%v.%v", randString, path.Base(contentType))
	filePath := filepath.Join(cfg.assetsRoot, filename)
	newFile, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create new file", err)
		fmt.Println(err, 432432)
		return
	}


	_, err = io.Copy(newFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create new file", err)
		return
	}

	newThumbnilURL := fmt.Sprintf("http://localhost:%v/assets/%v", cfg.port, filename)

	video.ThumbnailURL = &newThumbnilURL

	cfg.db.UpdateVideo(video)

	respondWithJSON(w, http.StatusOK, video)
}
