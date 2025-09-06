package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

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

	// Get the video's metadata from the database
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error fetching video metadata", err)
		return
	}
	if video.ID == uuid.Nil {
		respondWithError(w, http.StatusNotFound, "Video not found", nil)
		return
	}

	// Check if the authenticated user is the video owner
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You are not the owner of this video", nil)
		return
	}

	fmt.Println("uploading video for", videoID, "by user", userID)

	// Set upload limit to 1GB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	// Parse multipart form data
	const maxMemory = 10 << 20 // 10MB
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form", err)
		return
	}

	// Get the video file from the form
	file, fileHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error retrieving video file", err)
		return
	}
	defer file.Close()

	// Validate the uploaded file to ensure it's an MP4 video
	contentType := fileHeader.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type header", err)
		return
	}
	fmt.Println("Video media type:", mediaType)
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Only MP4 videos are allowed", nil)
		return
	}

	// Save the uploaded file to a temporary file on disk
	var tempFile *os.File
	tempFile, err = os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temp file", err)
		return
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()

	// Copy the contents from the uploaded file to the temp file
	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving to temp file", err)
		return
	}

	// Reset the tempFile's file pointer to the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error seeking temp file", err)
		return
	}

	// Generate a random file name for S3
	randBytes := make([]byte, 32)
	_, err = rand.Read(randBytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating random file name", err)
		return
	}
	randStr := base64.RawURLEncoding.EncodeToString(randBytes)
	fileExt := "mp4"

	// Get aspect ratio from temp file
	aspect, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting aspect ratio", err)
		return
	}
	var prefix string
	switch aspect {
	case "16:9":
		prefix = "landscape"
	case "9:16":
		prefix = "portrait"
	default:
		prefix = "other"
	}
	s3Key := fmt.Sprintf("%s/%s.%s", prefix, randStr, fileExt)

	// Process the temp video file for fast start
	processedPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return
	}

	// Open the processed file for upload
	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed video file", err)
		return
	}
	defer func() {
		processedFile.Close()
		os.Remove(processedPath)
	}()

	// Upload the processed file to S3
	putInput := &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &s3Key,
		Body:        processedFile,
		ContentType: &mediaType,
	}
	_, err = cfg.s3Client.PutObject(context.Background(), putInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading to S3", err)
		return
	}

	// Save the uploaded file to a temporary file on disk
	tempFile, err = os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temp file", err)
		return
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()

	// Copy the contents from the uploaded file to the temp file
	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving to temp file", err)
		return
	}

	// Reset the tempFile's file pointer to the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error seeking temp file", err)
		return
	}

	// Reset the tempFile's file pointer to the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error seeking temp file", err)
		return
	}

	// Store bucket and key as a comma delimited string in video_url
	videoURL := fmt.Sprintf("%s,%s", cfg.s3Bucket, s3Key)

	// Update the video metadata in DB
	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video metadata", err)
		return
	}

	// Generate presigned URL before returning video
	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error generating presigned URL", err)
		return
	}
	respondWithJSON(w, http.StatusOK, signedVideo)
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)
	presignInput := &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}
	presignOpts := s3.WithPresignExpires(expireTime)
	presignedReq, err := presignClient.PresignGetObject(context.Background(), presignInput, presignOpts)
	if err != nil {
		return "", err
	}
	return presignedReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	parts := splitOnComma(*video.VideoURL)
	if len(parts) != 2 {
		return video, fmt.Errorf("invalid video_url format")
	}
	bucket := parts[0]
	key := parts[1]
	url, err := generatePresignedURL(cfg.s3Client, bucket, key, 15*time.Minute)
	if err != nil {
		return video, err
	}
	video.VideoURL = &url
	return video, nil
}

func splitOnComma(s string) []string {
	var parts []string
	for i, ch := range s {
		if ch == ',' {
			parts = append(parts, s[:i], s[i+1:])
			break
		}
	}
	if len(parts) == 0 {
		parts = append(parts, s)
	}
	return parts
}
