package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type apiConfig struct {
	db               database.Client
	jwtSecret        string
	platform         string
	filepathRoot     string
	assetsRoot       string
	s3Bucket         string
	s3Region         string
	s3CfDistribution string
	port             string
	s3Client         *s3.Client
}

func main() {
	godotenv.Load(".env")

	pathToDB := os.Getenv("DB_PATH")
	if pathToDB == "" {
		log.Fatal("DB_URL must be set")
	}

	db, err := database.NewClient(pathToDB)
	if err != nil {
		log.Fatalf("Couldn't connect to database: %v", err)
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET environment variable is not set")
	}

	platform := os.Getenv("PLATFORM")
	if platform == "" {
		log.Fatal("PLATFORM environment variable is not set")
	}

	filepathRoot := os.Getenv("FILEPATH_ROOT")
	if filepathRoot == "" {
		log.Fatal("FILEPATH_ROOT environment variable is not set")
	}

	assetsRoot := os.Getenv("ASSETS_ROOT")
	if assetsRoot == "" {
		log.Fatal("ASSETS_ROOT environment variable is not set")
	}

	s3Bucket := os.Getenv("S3_BUCKET")
	if s3Bucket == "" {
		log.Fatal("S3_BUCKET environment variable is not set")
	}

	s3Region := os.Getenv("S3_REGION")
	if s3Region == "" {
		log.Fatal("S3_REGION environment variable is not set")
	}

	s3CfDistribution := os.Getenv("S3_CF_DISTRO")
	if s3CfDistribution == "" {
		log.Fatal("S3_CF_DISTRO environment variable is not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		log.Fatal("PORT environment variable is not set")
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(s3Region))
	if err != nil {
		log.Fatalf("Couldn't load AWS config: %v", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)

	cfg := apiConfig{
		db:               db,
		jwtSecret:        jwtSecret,
		platform:         platform,
		filepathRoot:     filepathRoot,
		assetsRoot:       assetsRoot,
		s3Bucket:         s3Bucket,
		s3Region:         s3Region,
		s3CfDistribution: s3CfDistribution,
		port:             port,
		s3Client:         s3Client,
	}

	err = cfg.ensureAssetsDir()
	if err != nil {
		log.Fatalf("Couldn't create assets directory: %v", err)
	}

	mux := http.NewServeMux()
	appHandler := http.StripPrefix("/app", http.FileServer(http.Dir(filepathRoot)))
	mux.Handle("/app/", appHandler)

	assetsHandler := http.StripPrefix("/assets", http.FileServer(http.Dir(assetsRoot)))
	mux.Handle("/assets/", noCacheMiddleware(assetsHandler))

	mux.HandleFunc("POST /api/login", cfg.handlerLogin)
	mux.HandleFunc("POST /api/refresh", cfg.handlerRefresh)
	mux.HandleFunc("POST /api/revoke", cfg.handlerRevoke)

	mux.HandleFunc("POST /api/users", cfg.handlerUsersCreate)

	mux.HandleFunc("POST /api/videos", cfg.handlerVideoMetaCreate)
	mux.HandleFunc("POST /api/thumbnail_upload/{videoID}", cfg.handlerUploadThumbnail)
	mux.HandleFunc("POST /api/video_upload/{videoID}", cfg.handlerUploadVideo)
	mux.HandleFunc("GET /api/videos", cfg.handlerVideosRetrieve)
	mux.HandleFunc("GET /api/videos/{videoID}", cfg.handlerVideoGet)
	mux.HandleFunc("DELETE /api/videos/{videoID}", cfg.handlerVideoMetaDelete)

	mux.HandleFunc("POST /admin/reset", cfg.handlerReset)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	log.Printf("Serving on: http://localhost:%s/app/\n", port)
	log.Fatal(srv.ListenAndServe())
}

type ffprobeStreams struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	var probe ffprobeStreams
	if err := json.Unmarshal(out.Bytes(), &probe); err != nil {
		return "", err
	}
	if len(probe.Streams) == 0 || probe.Streams[0].Width == 0 || probe.Streams[0].Height == 0 {
		return "other", nil
	}
	w := probe.Streams[0].Width
	h := probe.Streams[0].Height
	ratio := float64(w) / float64(h)
	if ratio > 1.7 && ratio < 1.8 {
		return "16:9", nil
	}
	if ratio < 0.6 {
		return "9:16", nil
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outPath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outPath)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return outPath, nil
}
