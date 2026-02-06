package main

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func generatePresignedURL(
	s3Client *s3.Client,
	bucket string,
	key string,
	expire time.Duration,
) (string, error) {

	presignClient := s3.NewPresignClient(s3Client)

	req, err := presignClient.PresignGetObject(
		context.TODO(),
		&s3.GetObjectInput{
			Bucket: &bucket,
			Key:    &key,
		},
		s3.WithPresignExpires(expire),
	)
	if err != nil {
		return "", err
	}

	return req.URL, nil
}
