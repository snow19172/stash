package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

const (
	watcherDebounce   = 2 * time.Second
	watcherStablePoll = time.Second
	watcherStableFor  = 3 * time.Second
	watcherStableMax  = 30 * time.Minute
)

type fileWatcher struct {
	watcher  *fsnotify.Watcher
	cancel   context.CancelFunc
	done     chan struct{}
	scanDone chan struct{}

	timersMu sync.Mutex
	timers   map[string]*time.Timer

	pendingMu    sync.Mutex
	scanQueue    chan string
	pendingFiles map[string]bool
}

func (fw *fileWatcher) startWorker(ctx context.Context, fn func()) {
	if ctx.Err() != nil {
		return
	}

	go fn()
}

func (fw *fileWatcher) stopTimers() {
	fw.timersMu.Lock()
	defer fw.timersMu.Unlock()
	for _, timer := range fw.timers {
		timer.Stop()
	}
}

func (s *Manager) RefreshFileWatcher() {
	s.fileWatcherMu.Lock()
	defer s.fileWatcherMu.Unlock()

	s.stopFileWatcherLocked()
	var roots []string
	for _, root := range s.Config.GetStashPaths() {
		if root == nil {
			continue
		}
		for _, library := range s.Config.GetAutoIdentifyLibraries() {
			if library != nil && library.AutoScan && filepath.Clean(library.Path) == filepath.Clean(root.Path) {
				roots = append(roots, root.Path)
				break
			}
		}
	}
	if len(roots) == 0 {
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Errorf("could not create library file watcher: %v", err)
		return
	}

	var watchedRoots []string
	for _, root := range roots {
		if err := watcher.Add(root); err != nil {
			logger.Warnf("could not watch library path %q: %v", root, err)
			continue
		}
		watchedRoots = append(watchedRoots, root)
	}
	if len(watchedRoots) == 0 {
		_ = watcher.Close()
		logger.Warn("automatic scanning is enabled, but no library directories could be watched")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	watchStarted := time.Now()
	fw := &fileWatcher{
		watcher:      watcher,
		cancel:       cancel,
		done:         make(chan struct{}),
		scanDone:     make(chan struct{}),
		timers:       make(map[string]*time.Timer),
		scanQueue:    make(chan string, 128),
		pendingFiles: make(map[string]bool),
	}
	s.fileWatcher = fw
	go s.runFileWatcher(ctx, fw)
	go s.runWatchedFileWorker(ctx, fw)
	for _, root := range watchedRoots {
		root := root
		fw.startWorker(ctx, func() {
			directories := 0
			if err := addWatchTreeContext(ctx, watcher, root, &directories); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warnf("could not watch library path %q: %v", root, err)
			}
			if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !s.isWatchableFile(path) {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if !info.ModTime().Before(watchStarted) {
					s.scheduleWatchedFile(ctx, path, fw)
				}
				return nil
			}); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warnf("could not reconcile files added while watching library path %q: %v", root, err)
			}
			logger.Infof("Registered %d directories under library path %q", directories, root)
		})
	}
	logger.Infof("Started automatic scanning for %d library directories", len(watchedRoots))
}

func (s *Manager) stopFileWatcher() {
	s.fileWatcherMu.Lock()
	defer s.fileWatcherMu.Unlock()
	s.stopFileWatcherLocked()
}

func (s *Manager) stopFileWatcherLocked() {
	if s.fileWatcher == nil {
		return
	}

	fw := s.fileWatcher
	s.fileWatcher = nil
	fw.cancel()
	if err := fw.watcher.Close(); err != nil {
		logger.Warnf("error closing library file watcher: %v", err)
	}
	<-fw.done
	<-fw.scanDone
}

func addWatchTreeContext(ctx context.Context, watcher *fsnotify.Watcher, root string, count *int) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("library path is not a directory")
	}

	var failedAdds int
	var failedWalks int
	var firstAddErr error
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			failedWalks++
			if firstAddErr == nil {
				firstAddErr = walkErr
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			failedAdds++
			if firstAddErr == nil {
				firstAddErr = err
			}
			return nil
		}
		(*count)++
		return nil
	})
	if err != nil {
		return err
	}
	if failedAdds > 0 || failedWalks > 0 {
		return fmt.Errorf("watch setup had %d registration failures and %d traversal failures: %w", failedAdds, failedWalks, firstAddErr)
	}
	return nil
}

func (s *Manager) runFileWatcher(ctx context.Context, fw *fileWatcher) {
	defer func() {
		fw.cancel()
		fw.stopTimers()
		close(fw.done)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-fw.watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					eventPath := event.Name
					fw.startWorker(ctx, func() {
						count := 0
						if err := addWatchTreeContext(ctx, fw.watcher, eventPath, &count); err != nil && !errors.Is(err, context.Canceled) {
							logger.Warnf("could not watch newly created directory %q: %v", eventPath, err)
						}
						if err := filepath.WalkDir(eventPath, func(path string, entry fs.DirEntry, walkErr error) error {
							if ctx.Err() != nil {
								return ctx.Err()
							}
							if walkErr != nil {
								return walkErr
							}
							if !entry.IsDir() && s.isWatchableFile(path) {
								s.scheduleWatchedFile(ctx, path, fw)
							}
							return nil
						}); err != nil && !errors.Is(err, context.Canceled) {
							logger.Warnf("could not scan new files under directory %q: %v", eventPath, err)
						}
					})
					continue
				}
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			if !s.isWatchableFile(event.Name) {
				continue
			}

			s.scheduleWatchedFile(ctx, event.Name, fw)
		case err, ok := <-fw.watcher.Errors:
			if !ok {
				return
			}
			logger.Errorf("library file watcher error: %v", err)
		}
	}
}

func (s *Manager) scheduleWatchedFile(ctx context.Context, path string, fw *fileWatcher) {
	path = filepath.Clean(path)
	fw.timersMu.Lock()
	if ctx.Err() != nil {
		fw.timersMu.Unlock()
		return
	}
	if timer, exists := fw.timers[path]; exists {
		timer.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(watcherDebounce, func() {
		fw.timersMu.Lock()
		if fw.timers[path] != timer {
			fw.timersMu.Unlock()
			return
		}
		delete(fw.timers, path)
		fw.timersMu.Unlock()
		s.queueWatchedFile(ctx, path, fw)
	})
	fw.timers[path] = timer
	fw.timersMu.Unlock()
}

func (s *Manager) isWatchableFile(path string) bool {
	if isZip(path) {
		return true
	}
	return useAsVideo(path) || useAsImage(path)
}

func (s *Manager) queueWatchedFile(ctx context.Context, path string, fw *fileWatcher) {
	if ctx.Err() != nil {
		return
	}

	fw.pendingMu.Lock()
	if _, exists := fw.pendingFiles[path]; exists {
		fw.pendingFiles[path] = true
		fw.pendingMu.Unlock()
		return
	}
	fw.pendingFiles[path] = false
	fw.pendingMu.Unlock()

	select {
	case fw.scanQueue <- path:
	case <-ctx.Done():
		fw.removePendingFile(path)
	}
}

func (fw *fileWatcher) completePendingFile(path string) bool {
	fw.pendingMu.Lock()
	defer fw.pendingMu.Unlock()

	if fw.pendingFiles[path] {
		fw.pendingFiles[path] = false
		return true
	}
	delete(fw.pendingFiles, path)
	return false
}

func (fw *fileWatcher) removePendingFile(path string) {
	fw.pendingMu.Lock()
	defer fw.pendingMu.Unlock()
	delete(fw.pendingFiles, path)
}

func (s *Manager) runWatchedFileWorker(ctx context.Context, fw *fileWatcher) {
	defer close(fw.scanDone)

	var retryPath string
	hasRetry := false
	for {
		var path string
		if hasRetry {
			path = retryPath
			hasRetry = false
		} else {
			select {
			case <-ctx.Done():
				return
			case path = <-fw.scanQueue:
				if ctx.Err() != nil {
					return
				}
			}
		}

		done := make(chan struct{})
		jobID := s.JobManager.Start(ctx, "Scanning newly added file: "+filepath.Base(path), job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
			defer close(done)
			return s.scanWatchedFileJob(ctx, progress, path)
		}))

		select {
		case <-done:
		case <-ctx.Done():
			s.JobManager.CancelJob(jobID)
			<-done
			fw.removePendingFile(path)
			return
		}

		if fw.completePendingFile(path) {
			retryPath = path
			hasRetry = true
		}
	}
}

func (s *Manager) scanWatchedFileJob(ctx context.Context, progress *job.Progress, path string) error {
	if err := waitForStableFile(ctx, path); err != nil {
		return err
	}

	scanOptions, err := s.getWatchScanOptions()
	if err != nil {
		return err
	}
	generateOptions := makeGenerateOptions(ScanMetadataInput{ScanMetadataOptions: scanOptions})
	if err := s.validateFFmpeg(); err != nil {
		return err
	}
	library := s.autoIdentifyLibraryForPath(path)
	waitForPhashes := library != nil && len(library.Sources) > 0 && scanOptions.ScanGeneratePhashes
	scannedFile, err := s.scanWatchedFile(ctx, progress, path, generateOptions, waitForPhashes)
	if err != nil {
		return fmt.Errorf("scanning newly added file %q: %w", path, err)
	}

	if !isVideo(path) {
		return nil
	}
	return s.autoIdentifyScannedFile(ctx, progress, scannedFile)
}

func (s *Manager) scanWatchedFile(ctx context.Context, progress *job.Progress, path string, generateOptions ScanGenerateOptions, waitForGeneration bool) (models.File, error) {
	taskQueue := s.startGenerateJob(ctx, "Generating for newly scanned file...")
	closed := false
	defer func() {
		if !closed {
			taskQueue.Close()
		}
	}()

	scanner := s.makeScanner(false, time.Time{})
	scanner.FileHandlers = getScanHandlers(generateOptions, taskQueue)
	scanJob := ScanJob{
		scanner:         scanner,
		paths:           []string{path},
		generateOptions: generateOptions,
		taskQueue:       taskQueue,
		subscriptions:   s.scanSubs,
	}

	var scannedFile models.File
	var err error
	if isZip(path) {
		err = scanJob.Execute(ctx, progress)
	} else {
		scannedFile, err = scanJob.scanFile(ctx, path)
		s.scanSubs.notify()
	}

	taskQueue.Close()
	closed = true
	if waitForGeneration {
		taskQueue.Wait()
	}
	return scannedFile, err
}

func waitForStableFile(ctx context.Context, path string) error {
	deadline := time.NewTimer(watcherStableMax)
	defer deadline.Stop()
	ticker := time.NewTicker(watcherStablePoll)
	defer ticker.Stop()

	var lastSize int64 = -1
	var lastModTime time.Time
	stableSince := time.Time{}
	for {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("checking watched file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() == lastSize && info.ModTime().Equal(lastModTime) {
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= watcherStableFor {
				return nil
			}
		} else {
			lastSize = info.Size()
			lastModTime = info.ModTime()
			stableSince = time.Time{}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("file %q did not become stable within %s", path, watcherStableMax)
		case <-ticker.C:
		}
	}
}

func (s *Manager) getWatchScanOptions() (config.ScanMetadataOptions, error) {
	if ui := s.Config.GetUIConfiguration(); ui != nil {
		if taskDefaults, ok := ui["taskDefaults"].(map[string]interface{}); ok {
			if scan, ok := taskDefaults["scan"]; ok {
				data, err := json.Marshal(scan)
				if err != nil {
					return config.ScanMetadataOptions{}, fmt.Errorf("encoding saved scan settings: %w", err)
				}
				var ret config.ScanMetadataOptions
				if err := json.Unmarshal(data, &ret); err != nil {
					return config.ScanMetadataOptions{}, fmt.Errorf("decoding saved scan settings: %w", err)
				}
				return ret, nil
			}
		}
	}

	if defaults := s.Config.GetDefaultScanSettings(); defaults != nil {
		return *defaults, nil
	}
	return config.ScanMetadataOptions{ScanGenerateCovers: true}, nil
}

func (s *Manager) autoIdentifyScannedFile(ctx context.Context, progress *job.Progress, scannedFile models.File) error {
	if scannedFile == nil {
		return nil
	}
	rule := s.autoIdentifyLibraryForPath(scannedFile.Base().Path)
	if rule == nil || len(rule.Sources) == 0 {
		return nil
	}
	var scenes []*models.Scene
	if err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
		var err error
		scenes, err = s.Repository.Scene.FindByFileID(ctx, scannedFile.Base().ID)
		return err
	}); err != nil {
		return fmt.Errorf("finding scene for auto-identification: %w", err)
	}
	if len(scenes) == 0 {
		return nil
	}
	if rule.IgnoreOrganizedScenes {
		scenes = slices.DeleteFunc(scenes, func(scene *models.Scene) bool {
			return !shouldAutoIdentifyScene(scene, true)
		})
	}
	if len(scenes) == 0 {
		return nil
	}

	return s.executeAutoIdentify(ctx, progress, rule, identify.Options{
		SceneIDs: []string{strconv.Itoa(scenes[0].ID)},
	})
}

func (s *Manager) autoIdentifyLibraryForPath(path string) *config.AutoIdentifyLibraryConfig {
	library := s.Config.GetStashPaths().GetStashFromDirPath(filepath.Dir(path))
	if library == nil {
		return nil
	}
	for _, rule := range s.Config.GetAutoIdentifyLibraries() {
		if rule != nil && rule.AutoIdentify && filepath.Clean(rule.Path) == filepath.Clean(library.Path) {
			return rule
		}
	}
	return nil
}

type autoIdentifyTarget struct {
	library *config.AutoIdentifyLibraryConfig
	paths   []string
}

func (s *Manager) autoIdentifyScanTargets(scanPaths []string) []autoIdentifyTarget {
	var ret []autoIdentifyTarget
	stashPaths := s.Config.GetStashPaths()
	for _, library := range s.Config.GetAutoIdentifyLibraries() {
		if library == nil || !library.AutoIdentify || len(library.Sources) == 0 {
			continue
		}

		var configured bool
		for _, stash := range stashPaths {
			if stash != nil && filepath.Clean(stash.Path) == filepath.Clean(library.Path) {
				configured = true
				break
			}
		}
		if !configured {
			continue
		}

		paths := pathsForLibraryScan(scanPaths, library.Path)
		if len(paths) > 0 {
			ret = append(ret, autoIdentifyTarget{library: library, paths: paths})
		}
	}
	return ret
}

func pathsForLibraryScan(scanPaths []string, libraryPath string) []string {
	libraryRoot, err := filepath.Abs(filepath.Clean(libraryPath))
	if err != nil {
		return nil
	}
	if len(scanPaths) == 0 {
		return []string{libraryRoot}
	}

	var ret []string
	for _, scanPath := range scanPaths {
		absScanPath, err := filepath.Abs(filepath.Clean(scanPath))
		if err != nil {
			continue
		}
		if info, err := os.Stat(absScanPath); err == nil && !info.IsDir() {
			absScanPath = filepath.Dir(absScanPath)
		}
		switch {
		case pathWithin(libraryRoot, absScanPath):
			ret = append(ret, absScanPath)
		case pathWithin(absScanPath, libraryRoot):
			ret = append(ret, libraryRoot)
		}
	}
	return ret
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (s *Manager) identifyScannedVideos(ctx context.Context, progress *job.Progress, videos []scannedVideoFile, targets []autoIdentifyTarget) error {
	for _, target := range targets {
		var sceneIDs []string
		sceneIDSet := make(map[string]bool)
		for _, video := range videos {
			videoPath, err := filepath.Abs(filepath.Clean(video.path))
			if err != nil {
				return fmt.Errorf("resolving scanned video path %q: %w", video.path, err)
			}
			inTarget := false
			for _, path := range target.paths {
				if pathWithin(path, videoPath) {
					inTarget = true
					break
				}
			}
			if !inTarget {
				continue
			}
			library := s.Config.GetStashPaths().GetStashFromDirPath(filepath.Dir(videoPath))
			if library == nil || filepath.Clean(library.Path) != filepath.Clean(target.library.Path) {
				continue
			}

			var scenes []*models.Scene
			if err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
				var err error
				scenes, err = s.Repository.Scene.FindByFileID(ctx, video.id)
				return err
			}); err != nil {
				return fmt.Errorf("finding scene for scanned video %q: %w", video.path, err)
			}
			for _, scene := range scenes {
				if !shouldAutoIdentifyScene(scene, target.library.IgnoreOrganizedScenes) {
					continue
				}
				sceneID := strconv.Itoa(scene.ID)
				if !sceneIDSet[sceneID] {
					sceneIDSet[sceneID] = true
					sceneIDs = append(sceneIDs, sceneID)
				}
			}
		}
		if len(sceneIDs) == 0 {
			continue
		}
		if err := s.executeAutoIdentify(ctx, progress, target.library, identify.Options{
			SceneIDs: sceneIDs,
		}); err != nil {
			return err
		}
	}
	return nil
}

func shouldAutoIdentifyScene(scene *models.Scene, ignoreOrganized bool) bool {
	return scene != nil && (!ignoreOrganized || !scene.Organized)
}

func (s *Manager) executeAutoIdentify(ctx context.Context, progress *job.Progress, library *config.AutoIdentifyLibraryConfig, input identify.Options) error {
	input.Sources = library.Sources
	input.Options = autoIdentifyMetadataOptions()
	j := CreateIdentifyJob(input)
	j.MergeSources = true
	j.OnlyFillMissing = true
	return j.Execute(ctx, progress)
}

func autoIdentifyMetadataOptions() *identify.MetadataOptions {
	fields := []string{
		"title", "code", "date", "production_date", "director", "details", "cover_image",
		"url", "studio", "performers", "tags", "stash_ids",
	}
	options := &identify.MetadataOptions{
		FieldOptions: make([]*identify.FieldOptions, 0, len(fields)),
	}
	options.SkipMultipleMatches = new(bool)
	*options.SkipMultipleMatches = true
	options.SkipSingleNamePerformers = new(bool)
	*options.SkipSingleNamePerformers = true
	for _, field := range fields {
		fieldOptions := &identify.FieldOptions{
			Field:    field,
			Strategy: identify.FieldStrategyMerge,
		}
		if field == "studio" || field == "performers" || field == "tags" {
			fieldOptions.CreateMissing = new(bool)
			*fieldOptions.CreateMissing = true
		}
		options.FieldOptions = append(options.FieldOptions, fieldOptions)
	}
	setCoverImage := true
	options.SetCoverImage = &setCoverImage
	return options
}
