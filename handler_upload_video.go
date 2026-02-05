package main

import (
	"os"
	"os/exec"
	"io"
	"fmt"
	"context"
	"mime"
	"net/http"
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
)

type ffprobeOutput struct {
	Streams []struct {
		Width int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command(
    "ffprobe",
    "-v", "error",
    "-print_format", "json",
    "-show_streams",
    filePath,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe failed: %w", err)
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &probe); err != nil {
		return "", fmt.Errorf("failed to parse ffprobe output: %w", err)
	}
	if len(probe.Streams) == 0 {
		return "", fmt.Errorf("no streams found")
	}

	width := probe.Streams[0].Width
	height := probe.Streams[0].Height

	if width == 0 || height == 0 {
		return "", fmt.Errorf("invalid video dimensions")
	}

	ratio := float64(width) / float64(height)

	if ratio > 1.7 && ratio < 1.8 {
		return "16:9", nil
	} else if ratio > 0.55 && ratio < 0.6 {
		return "9:16", nil
	} else {
		return "other", nil
	}
}

 func processVideoForFastStart(filePath string) (string, error) {
	 ofPath := filePath + ".processing"
	 cmd := exec.Command(
		 "ffmpeg",
		 "-i",
		 filePath,
		 "-c",
		 "copy",
		 "-movflags",
		 "faststart",
		 "-f",
		 "mp4",
		 ofPath,
	 )
	 if err := cmd.Run(); err != nil {
		 return "", fmt.Errorf("ffmpeg failed: %w", err)
	 }
	 return ofPath, nil
 }

func aspectToPrefix(r string) string {
	switch r {
	case "16:9":
		return "landscape"
	case "9:16":
		return "portrait"
	default:
		return "other"
	}
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "invalid id", err)
		return
	}
	
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "couln't find jwt", err)
		return
	}
	
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "couldn't validate jwt", err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1 << 30)

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "video not found", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "not the video owner", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "couldn't read video", err)
		return
	}

	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil || mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "file must an mp4 video", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't create temporary video file", err)
		return
	}
	defer os.Remove(tempFile.Name())

	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't save temp file", err)
		return
	}

	tempFile.Close()

	ratio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to get aspect ratio", err)
		return
	}

	prefix := aspectToPrefix(ratio)

	processedPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to process video", err)
		return
	}
	defer os.Remove(processedPath)

	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "couln't reopen processedFile", err)
		return
	}
	defer processedFile.Close()

	s3Key, err := randomFileName(".mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't generate s3 key", err)
		return
	}

	key := fmt.Sprintf("%s/%s", prefix, s3Key)

	// upload
	_, err = cfg.s3Client.PutObject(
		context.TODO(),
		&s3.PutObjectInput{
			Bucket:				&cfg.s3Bucket,
			Key:					&key,
			Body:					processedFile,
			ContentType:	&mediaType,
		},
	)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to upload to S3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)
	video.VideoURL = &videoURL

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't update video record", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
