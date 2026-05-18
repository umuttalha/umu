package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Client struct {
	mc     *minio.Client
	bucket string
}

type Metadata struct {
	Name       string    `json:"name"`
	CPUs       int       `json:"cpus"`
	MemoryMB   int       `json:"memory_mb"`
	DiskGB     int       `json:"disk_gb"`
	GlobalIP   string    `json:"global_ip"`
	CreatedAt  time.Time `json:"created_at"`
	ArchivedAt time.Time `json:"archived_at"`
	UmutVersion string   `json:"umut_version"`
}

func New(endpoint, accessKey, secretKey, bucket, region string) (*Client, error) {
	secure := true
	ep := endpoint
	if strings.HasPrefix(ep, "https://") {
		ep = strings.TrimPrefix(ep, "https://")
	} else if strings.HasPrefix(ep, "http://") {
		ep = strings.TrimPrefix(ep, "http://")
		secure = false
	}
	ep = strings.TrimSuffix(ep, "/")

	mc, err := minio.New(ep, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &Client{mc: mc, bucket: bucket}, nil
}

func (c *Client) Push(projectName, diskPath string, meta Metadata) error {
	ctx := context.Background()

	// Check bucket exists
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("bucket check: %w", err)
	}
	if !exists {
		return fmt.Errorf("bucket %q does not exist", c.bucket)
	}

	// Upload metadata JSON
	meta.ArchivedAt = time.Now()
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	metaKey := projectName + "/metadata.json"
	if _, err := c.mc.PutObject(ctx, c.bucket, metaKey, bytesReader(metaJSON), int64(len(metaJSON)), minio.PutObjectOptions{
		ContentType: "application/json",
	}); err != nil {
		return fmt.Errorf("upload metadata: %w", err)
	}

	// Gzip and upload disk
	diskKey := projectName + "/disk.ext4.gz"
	pr, pw := io.Pipe()
	gw := gzip.NewWriter(pw)

	uploadErr := make(chan error, 1)
	go func() {
		_, err := c.mc.PutObject(ctx, c.bucket, diskKey, pr, -1, minio.PutObjectOptions{
			ContentType: "application/gzip",
		})
		uploadErr <- err
	}()

	diskFile, err := os.Open(diskPath)
	if err != nil {
		pw.Close()
		return fmt.Errorf("open disk: %w", err)
	}
	defer diskFile.Close()

	if _, err := io.Copy(gw, diskFile); err != nil {
		gw.Close()
		pw.Close()
		return fmt.Errorf("gzip disk: %w", err)
	}
	gw.Close()
	pw.Close()

	if err := <-uploadErr; err != nil {
		return fmt.Errorf("upload disk: %w", err)
	}

	return nil
}

func (c *Client) Pull(projectName, destPath string) (*Metadata, error) {
	ctx := context.Background()

	// Download metadata
	metaKey := projectName + "/metadata.json"
	obj, err := c.mc.GetObject(ctx, c.bucket, metaKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get metadata: %w", err)
	}
	defer obj.Close()

	var meta Metadata
	if err := json.NewDecoder(obj).Decode(&meta); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	// Download and decompress disk
	diskKey := projectName + "/disk.ext4.gz"
	diskObj, err := c.mc.GetObject(ctx, c.bucket, diskKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get disk: %w", err)
	}
	defer diskObj.Close()

	gr, err := gzip.NewReader(diskObj)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	tmpPath := destPath + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(tmpFile, gr); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write disk: %w", err)
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("rename disk: %w", err)
	}

	os.Chown(destPath, 1000, 1000) // umut:umut for Firecracker jailer
	os.Chmod(destPath, 0640)

	return &meta, nil
}

func (c *Client) List() ([]string, error) {
	ctx := context.Background()

	objects := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    "",
		Recursive: false,
	})

	projects := make(map[string]bool)
	for obj := range objects {
		if obj.Err != nil {
			return nil, fmt.Errorf("list objects: %w", obj.Err)
		}
		name := strings.TrimSuffix(obj.Key, "/")
		if name != "" {
			projects[name] = true
		}
	}

	result := make([]string, 0, len(projects))
	for p := range projects {
		result = append(result, p)
	}
	return result, nil
}

func DiskPath(projectName string) string {
	dir := os.Getenv("UMUT_DATA_DIR")
	if dir == "" {
		dir = "/var/lib/umut"
	}
	return filepath.Join(dir, "images", projectName+".ext4")
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
