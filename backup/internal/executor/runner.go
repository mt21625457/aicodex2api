package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/backup/ent"
	"github.com/Wei-Shaw/sub2api/backup/ent/backupjob"
	"github.com/Wei-Shaw/sub2api/backup/internal/s3client"
	"github.com/Wei-Shaw/sub2api/backup/internal/store/entstore"
)

const (
	defaultPollInterval  = 5 * time.Second
	defaultRunTimeout    = 30 * time.Minute
	defaultEventTimeout  = 2 * time.Second
	defaultRootDirectory = "/var/lib/sub2api/backups"
)

type Options struct {
	PollInterval time.Duration
	RunTimeout   time.Duration
	Logger       *log.Logger
}

type Runner struct {
	store        *entstore.Store
	pollInterval time.Duration
	runTimeout   time.Duration
	logger       *log.Logger

	notifyCh chan struct{}
	stopCh   chan struct{}
	doneCh   chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
}

type runResult struct {
	Artifact   *entstore.BackupArtifactSnapshot
	S3Object   *entstore.BackupS3ObjectSnapshot
	PartialErr error
}

type generatedFile struct {
	ArchiveName string `json:"archive_name"`
	LocalPath   string `json:"local_path"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type bundleManifest struct {
	JobID      string          `json:"job_id"`
	BackupType string          `json:"backup_type"`
	SourceMode string          `json:"source_mode"`
	CreatedAt  string          `json:"created_at"`
	Files      []generatedFile `json:"files"`
}

func NewRunner(store *entstore.Store, opts Options) *Runner {
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	runTimeout := opts.RunTimeout
	if runTimeout <= 0 {
		runTimeout = defaultRunTimeout
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stdout, "[backupd-executor] ", log.LstdFlags)
	}

	return &Runner{
		store:        store,
		pollInterval: poll,
		runTimeout:   runTimeout,
		logger:       logger,
		notifyCh:     make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

func (r *Runner) Start() error {
	if r == nil || r.store == nil {
		return errors.New("executor store is required")
	}

	var startErr error
	r.startOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		requeued, err := r.store.RequeueRunningJobs(ctx)
		if err != nil {
			startErr = err
			return
		}
		if requeued > 0 {
			r.logger.Printf("requeued %d running jobs after restart", requeued)
		}

		go r.loop()
		r.Notify()
	})
	return startErr
}

func (r *Runner) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})

	if ctx == nil {
		<-r.doneCh
		return nil
	}

	select {
	case <-r.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) Notify() {
	if r == nil {
		return
	}
	select {
	case r.notifyCh <- struct{}{}:
	default:
	}
}

func (r *Runner) loop() {
	defer close(r.doneCh)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.notifyCh:
			r.processQueuedJobs()
		case <-ticker.C:
			r.processQueuedJobs()
		case <-r.stopCh:
			return
		}
	}
}

func (r *Runner) processQueuedJobs() {
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		job, err := r.store.AcquireNextQueuedJob(ctx)
		cancel()
		if err != nil {
			if ent.IsNotFound(err) {
				return
			}
			r.logger.Printf("acquire queued job failed: %v", err)
			return
		}

		r.executeJob(job)
	}
}

func (r *Runner) executeJob(job *ent.BackupJob) {
	if job == nil {
		return
	}

	r.logEvent(job.JobID, "info", "worker", "job picked by executor", "")

	ctx, cancel := context.WithTimeout(context.Background(), r.runTimeout)
	defer cancel()

	result, err := r.run(ctx, job)
	finishInput := entstore.FinishBackupJobInput{
		JobID:  job.JobID,
		Status: backupjob.StatusFailed.String(),
	}

	if err != nil {
		r.logger.Printf("job %s failed: %v", job.JobID, err)
		finishInput.ErrorMessage = shortenError(err)
	} else {
		finishInput.Artifact = result.Artifact
		finishInput.S3Object = result.S3Object
		switch {
		case result.PartialErr != nil:
			finishInput.Status = backupjob.StatusPartialSucceeded.String()
			finishInput.ErrorMessage = shortenError(result.PartialErr)
		default:
			finishInput.Status = backupjob.StatusSucceeded.String()
		}
	}

	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if _, finishErr := r.store.FinishBackupJob(finishCtx, finishInput); finishErr != nil {
		r.logger.Printf("job %s finish update failed: %v", job.JobID, finishErr)
	}
}

func (r *Runner) run(ctx context.Context, job *ent.BackupJob) (*runResult, error) {
	cfg, err := r.store.GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load config failed: %w", err)
	}

	backupRoot := normalizeBackupRoot(cfg.BackupRoot)
	jobDir := filepath.Join(
		backupRoot,
		time.Now().UTC().Format("2006"),
		time.Now().UTC().Format("01"),
		time.Now().UTC().Format("02"),
		job.JobID,
	)
	if err := os.MkdirAll(jobDir, 0o750); err != nil {
		return nil, fmt.Errorf("create backup directory failed: %w", err)
	}

	generated := make([]generatedFile, 0, 4)
	backupType := strings.TrimSpace(job.BackupType.String())

	if backupType == backupjob.BackupTypePostgres.String() || backupType == backupjob.BackupTypeFull.String() {
		postgresPath := filepath.Join(jobDir, "postgres.dump")
		if err := runPostgresBackup(ctx, cfg, postgresPath); err != nil {
			return nil, fmt.Errorf("postgres backup failed: %w", err)
		}
		gf, err := buildGeneratedFile("postgres.dump", postgresPath)
		if err != nil {
			return nil, err
		}
		generated = append(generated, gf)
		r.logEvent(job.JobID, "info", "artifact", "postgres backup finished", "")
	}

	if backupType == backupjob.BackupTypeRedis.String() || backupType == backupjob.BackupTypeFull.String() {
		redisPath := filepath.Join(jobDir, "redis.rdb")
		if err := runRedisBackup(ctx, cfg, redisPath, job.JobID); err != nil {
			return nil, fmt.Errorf("redis backup failed: %w", err)
		}
		gf, err := buildGeneratedFile("redis.rdb", redisPath)
		if err != nil {
			return nil, err
		}
		generated = append(generated, gf)
		r.logEvent(job.JobID, "info", "artifact", "redis backup finished", "")
	}

	manifest := bundleManifest{
		JobID:      job.JobID,
		BackupType: backupType,
		SourceMode: strings.TrimSpace(cfg.SourceMode),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Files:      generated,
	}
	manifestPath := filepath.Join(jobDir, "manifest.json")
	if err := writeManifest(manifestPath, manifest); err != nil {
		return nil, fmt.Errorf("write manifest failed: %w", err)
	}
	manifestGenerated, err := buildGeneratedFile("manifest.json", manifestPath)
	if err != nil {
		return nil, err
	}
	generated = append(generated, manifestGenerated)

	bundlePath := filepath.Join(jobDir, "bundle.tar.gz")
	if err := writeBundle(bundlePath, generated); err != nil {
		return nil, fmt.Errorf("build bundle failed: %w", err)
	}
	bundleSize, bundleSHA, err := fileDigest(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("bundle hash failed: %w", err)
	}
	r.logEvent(job.JobID, "info", "artifact", "bundle generated", "")

	result := &runResult{
		Artifact: &entstore.BackupArtifactSnapshot{
			LocalPath: bundlePath,
			SizeBytes: bundleSize,
			SHA256:    bundleSHA,
		},
	}

	if job.UploadToS3 {
		r.logEvent(job.JobID, "info", "s3", "start upload to s3", "")
		s3Object, uploadErr := uploadToS3(ctx, cfg, job.JobID, bundlePath)
		if uploadErr != nil {
			result.PartialErr = fmt.Errorf("upload s3 failed: %w", uploadErr)
			r.logEvent(job.JobID, "warning", "s3", "upload to s3 failed", shortenError(uploadErr))
		} else {
			result.S3Object = s3Object
			r.logEvent(job.JobID, "info", "s3", "upload to s3 finished", "")
		}
	}

	if err := applyRetentionPolicy(ctx, r.store, cfg); err != nil {
		r.logger.Printf("retention cleanup failed: %v", err)
	}

	return result, nil
}

func runPostgresBackup(ctx context.Context, cfg *entstore.ConfigSnapshot, destination string) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	mode := normalizeSourceMode(cfg.SourceMode)
	pg := cfg.Postgres
	host := defaultIfBlank(pg.Host, "127.0.0.1")
	port := pg.Port
	if port <= 0 {
		port = 5432
	}
	user := defaultIfBlank(pg.User, "postgres")
	database := strings.TrimSpace(pg.Database)
	if database == "" {
		return errors.New("postgres.database is required")
	}

	baseArgs := []string{
		"-h", host,
		"-p", strconv.Itoa(int(port)),
		"-U", user,
		"-d", database,
		"--format=custom",
		"--no-owner",
		"--no-privileges",
	}
	if strings.TrimSpace(pg.SSLMode) != "" {
		baseArgs = append(baseArgs, "--sslmode", strings.TrimSpace(pg.SSLMode))
	}

	switch mode {
	case "direct":
		args := append([]string{}, baseArgs...)
		args = append(args, "--file", destination)
		env := []string{}
		if strings.TrimSpace(pg.Password) != "" {
			env = append(env, "PGPASSWORD="+strings.TrimSpace(pg.Password))
		}
		return runCommand(ctx, "pg_dump", args, env, nil)
	case "docker_exec":
		container := strings.TrimSpace(pg.ContainerName)
		if container == "" {
			return errors.New("postgres.container_name is required in docker_exec mode")
		}
		outputFile, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		defer func() {
			_ = outputFile.Close()
		}()

		args := []string{"exec"}
		if strings.TrimSpace(pg.Password) != "" {
			args = append(args, "-e", "PGPASSWORD="+strings.TrimSpace(pg.Password))
		}
		args = append(args, container, "pg_dump")
		args = append(args, baseArgs...)
		return runCommand(ctx, "docker", args, nil, outputFile)
	default:
		return fmt.Errorf("unsupported source_mode: %s", mode)
	}
}

func runRedisBackup(ctx context.Context, cfg *entstore.ConfigSnapshot, destination, jobID string) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	mode := normalizeSourceMode(cfg.SourceMode)
	redisCfg := cfg.Redis

	host, port := parseRedisAddr(redisCfg.Addr)
	baseArgs := []string{}
	if host != "" {
		baseArgs = append(baseArgs, "-h", host)
	}
	if port > 0 {
		baseArgs = append(baseArgs, "-p", strconv.Itoa(port))
	}
	if strings.TrimSpace(redisCfg.Username) != "" {
		baseArgs = append(baseArgs, "--user", strings.TrimSpace(redisCfg.Username))
	}
	if redisCfg.DB >= 0 {
		baseArgs = append(baseArgs, "-n", strconv.Itoa(int(redisCfg.DB)))
	}

	env := []string{}
	if strings.TrimSpace(redisCfg.Password) != "" {
		env = append(env, "REDISCLI_AUTH="+strings.TrimSpace(redisCfg.Password))
	}

	switch mode {
	case "direct":
		args := append([]string{}, baseArgs...)
		args = append(args, "--rdb", destination)
		return runCommand(ctx, "redis-cli", args, env, nil)
	case "docker_exec":
		container := strings.TrimSpace(redisCfg.ContainerName)
		if container == "" {
			return errors.New("redis.container_name is required in docker_exec mode")
		}
		tmpPath := fmt.Sprintf("/tmp/sub2api_%s.rdb", sanitizeFileName(jobID))

		execArgs := []string{"exec"}
		for _, item := range env {
			execArgs = append(execArgs, "-e", item)
		}
		execArgs = append(execArgs, container, "redis-cli")
		execArgs = append(execArgs, baseArgs...)
		execArgs = append(execArgs, "--rdb", tmpPath)
		if err := runCommand(ctx, "docker", execArgs, nil, nil); err != nil {
			return err
		}

		copyArgs := []string{"cp", container + ":" + tmpPath, destination}
		if err := runCommand(ctx, "docker", copyArgs, nil, nil); err != nil {
			_ = runCommand(ctx, "docker", []string{"exec", container, "rm", "-f", tmpPath}, nil, nil)
			return err
		}
		_ = runCommand(ctx, "docker", []string{"exec", container, "rm", "-f", tmpPath}, nil, nil)
		return nil
	default:
		return fmt.Errorf("unsupported source_mode: %s", mode)
	}
}

func uploadToS3(ctx context.Context, cfg *entstore.ConfigSnapshot, jobID, bundlePath string) (*entstore.BackupS3ObjectSnapshot, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if !cfg.S3.Enabled {
		return nil, errors.New("s3 is disabled")
	}
	if strings.TrimSpace(cfg.S3.Bucket) == "" {
		return nil, errors.New("s3.bucket is required")
	}
	if strings.TrimSpace(cfg.S3.Region) == "" {
		return nil, errors.New("s3.region is required")
	}

	client, err := s3client.New(ctx, s3client.Config{
		Endpoint:        strings.TrimSpace(cfg.S3.Endpoint),
		Region:          strings.TrimSpace(cfg.S3.Region),
		AccessKeyID:     strings.TrimSpace(cfg.S3.AccessKeyID),
		SecretAccessKey: strings.TrimSpace(cfg.S3.SecretAccessKey),
		Bucket:          strings.TrimSpace(cfg.S3.Bucket),
		Prefix:          strings.Trim(strings.TrimSpace(cfg.S3.Prefix), "/"),
		ForcePathStyle:  cfg.S3.ForcePathStyle,
		UseSSL:          cfg.S3.UseSSL,
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	key := joinS3Key(
		client.Prefix(),
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"),
		jobID,
		filepath.Base(bundlePath),
	)
	etag, err := client.UploadFile(ctx, bundlePath, key)
	if err != nil {
		return nil, err
	}
	return &entstore.BackupS3ObjectSnapshot{
		Bucket: client.Bucket(),
		Key:    key,
		ETag:   etag,
	}, nil
}

func applyRetentionPolicy(ctx context.Context, store *entstore.Store, cfg *entstore.ConfigSnapshot) error {
	if store == nil || cfg == nil {
		return nil
	}
	keepLast := int(cfg.KeepLast)
	retentionDays := int(cfg.RetentionDays)
	if keepLast <= 0 && retentionDays <= 0 {
		return nil
	}

	items, err := store.ListFinishedJobsForRetention(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	threshold := time.Now().AddDate(0, 0, -retentionDays)
	for idx, item := range items {
		if item == nil {
			continue
		}
		keepByCount := keepLast > 0 && idx < keepLast
		keepByTime := false
		if retentionDays > 0 {
			reference := item.CreatedAt
			if item.FinishedAt != nil {
				reference = *item.FinishedAt
			}
			keepByTime = reference.After(threshold)
		}
		if keepByCount || keepByTime {
			continue
		}

		artifactPath := strings.TrimSpace(item.ArtifactLocalPath)
		if artifactPath == "" {
			continue
		}
		if err := os.RemoveAll(filepath.Dir(artifactPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func buildGeneratedFile(archiveName, path string) (generatedFile, error) {
	size, sum, err := fileDigest(path)
	if err != nil {
		return generatedFile{}, err
	}
	return generatedFile{
		ArchiveName: archiveName,
		LocalPath:   path,
		SizeBytes:   size,
		SHA256:      sum,
	}, nil
}

func writeManifest(path string, manifest bundleManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o640)
}

func writeBundle(path string, files []generatedFile) error {
	output, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer func() {
		_ = output.Close()
	}()

	gzipWriter := gzip.NewWriter(output)
	defer func() {
		_ = gzipWriter.Close()
	}()

	tarWriter := tar.NewWriter(gzipWriter)
	defer func() {
		_ = tarWriter.Close()
	}()

	for _, file := range files {
		info, err := os.Stat(file.LocalPath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = file.ArchiveName
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		reader, err := os.Open(file.LocalPath)
		if err != nil {
			return err
		}
		if _, err = io.Copy(tarWriter, reader); err != nil {
			_ = reader.Close()
			return err
		}
		_ = reader.Close()
	}
	return nil
}

func fileDigest(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		_ = file.Close()
	}()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func runCommand(ctx context.Context, name string, args []string, extraEnv []string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	if stdout != nil {
		cmd.Stdout = stdout
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return fmt.Errorf("%s command failed: %s", name, sanitizeError(errMsg))
	}
	return nil
}

func (r *Runner) logEvent(jobID, level, eventType, message, payload string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultEventTimeout)
	defer cancel()
	if err := r.store.AppendJobEvent(ctx, jobID, level, eventType, message, payload); err != nil {
		r.logger.Printf("append event failed, job=%s event=%s err=%v", jobID, eventType, err)
	}
}

func normalizeSourceMode(v string) string {
	mode := strings.TrimSpace(v)
	if mode == "" {
		return "direct"
	}
	return mode
}

func normalizeBackupRoot(root string) string {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return defaultRootDirectory
	}
	return trimmed
}

func parseRedisAddr(addr string) (string, int) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return "127.0.0.1", 6379
	}

	host, portText, err := net.SplitHostPort(trimmed)
	if err != nil {
		return trimmed, 6379
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return host, 6379
	}
	return host, port
}

func joinS3Key(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.Trim(strings.TrimSpace(part), "/")
		if p == "" {
			continue
		}
		filtered = append(filtered, p)
	}
	return strings.Join(filtered, "/")
}

func sanitizeFileName(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "job"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_")
	return replacer.Replace(trimmed)
}

func sanitizeError(v string) string {
	out := strings.TrimSpace(v)
	out = strings.ReplaceAll(out, "\n", " ")
	out = strings.ReplaceAll(out, "\r", " ")
	out = strings.TrimSpace(out)
	if out == "" {
		return "unknown error"
	}
	if len(out) > 512 {
		return out[:512]
	}
	return out
}

func shortenError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeError(err.Error())
}

func defaultIfBlank(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
