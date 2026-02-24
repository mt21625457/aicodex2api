package executor

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRedisAddr(t *testing.T) {
	t.Parallel()

	host, port := parseRedisAddr("127.0.0.1:6380")
	require.Equal(t, "127.0.0.1", host)
	require.Equal(t, 6380, port)

	host, port = parseRedisAddr("localhost")
	require.Equal(t, "localhost", host)
	require.Equal(t, 6379, port)

	host, port = parseRedisAddr("")
	require.Equal(t, "127.0.0.1", host)
	require.Equal(t, 6379, port)
}

func TestJoinS3Key(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a/b/c", joinS3Key("/a/", "/b", "c/"))
	require.Equal(t, "a/c", joinS3Key("a", "", "c"))
	require.Equal(t, "", joinS3Key("", " ", "/"))
}

func TestSanitizeError(t *testing.T) {
	t.Parallel()

	msg := sanitizeError("line1\nline2\rline3")
	require.Equal(t, "line1 line2 line3", msg)

	longMsg := sanitizeError(strings.Repeat("x", 600))
	require.Len(t, longMsg, 512)
}

func TestWriteManifestAndBundle(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	fileAPath := filepath.Join(workDir, "postgres.dump")
	fileBPath := filepath.Join(workDir, "redis.rdb")
	require.NoError(t, os.WriteFile(fileAPath, []byte("postgres-data"), 0o640))
	require.NoError(t, os.WriteFile(fileBPath, []byte("redis-data"), 0o640))

	fileA, err := buildGeneratedFile("postgres.dump", fileAPath)
	require.NoError(t, err)
	fileB, err := buildGeneratedFile("redis.rdb", fileBPath)
	require.NoError(t, err)

	manifestPath := filepath.Join(workDir, "manifest.json")
	require.NoError(t, writeManifest(manifestPath, bundleManifest{
		JobID:      "bk_demo",
		BackupType: "full",
		SourceMode: "direct",
		CreatedAt:  "2026-01-01T00:00:00Z",
		Files:      []generatedFile{fileA, fileB},
	}))
	manifestFile, err := buildGeneratedFile("manifest.json", manifestPath)
	require.NoError(t, err)

	bundlePath := filepath.Join(workDir, "bundle.tar.gz")
	require.NoError(t, writeBundle(bundlePath, []generatedFile{fileA, fileB, manifestFile}))

	entries, err := readTarEntries(bundlePath)
	require.NoError(t, err)
	require.Contains(t, entries, "postgres.dump")
	require.Contains(t, entries, "redis.rdb")
	require.Contains(t, entries, "manifest.json")
}

func readTarEntries(bundlePath string) ([]string, error) {
	file, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gzReader.Close() }()

	tarReader := tar.NewReader(gzReader)
	entries := make([]string, 0, 8)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, header.Name)
	}
	return entries, nil
}
