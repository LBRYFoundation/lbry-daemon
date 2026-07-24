package fileanalysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

const unavailableMessage = "Unable to locate or run ffmpeg or ffprobe. Please install FFmpeg and ensure that it is callable via PATH or conf.ffmpeg_path"

var (
	fastStartPattern  = regexp.MustCompile(`Before avformat_find_stream_info.+?\s+seeks:(\d+)\s+`)
	meanVolumePattern = regexp.MustCompile(`mean_volume:\s+([-+]?\d*\.\d+|\d+)`)
	maxVolumePattern  = regexp.MustCompile(`max_volume:\s+([-+]?\d*\.\d+|\d+)`)
)

type Config struct {
	FFmpegPath         string
	DataDir            string
	VideoEncoder       string
	VideoScaler        string
	AudioEncoder       string
	VolumeFilter       string
	VideoBitrateMax    int
	VolumeAnalysisTime int
}

type ConfigProvider func() Config

type RunFunc func(context.Context, string, ...string) (string, int, error)

type Option func(*Analyzer)

func WithRunner(runner RunFunc) Option {
	return func(analyzer *Analyzer) {
		if runner != nil {
			analyzer.run = runner
		}
	}
}

func WithLogger(logger func(string, ...any)) Option {
	return func(analyzer *Analyzer) {
		if logger != nil {
			analyzer.logf = logger
		}
	}
}

type Analyzer struct {
	config ConfigProvider
	run    RunFunc
	logf   func(string, ...any)

	mu                sync.Mutex
	checked           bool
	installed         bool
	ffmpeg            string
	ffprobe           string
	availableEncoders string
}

func New(provider ConfigProvider, options ...Option) *Analyzer {
	if provider == nil {
		provider = func() Config { return Config{} }
	}
	analyzer := &Analyzer{config: provider, run: runCommand, logf: log.Printf}
	for _, option := range options {
		option(analyzer)
	}
	return analyzer
}

func runCommand(ctx context.Context, executable string, arguments ...string) (string, int, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return string(output), exitError.ExitCode(), err
	}
	return string(output), -1, err
}

// Status reproduces VideoFileAnalyzer.status. Reset forces the next lookup to
// use the current configuration, while recheck bypasses the cached result.
func (analyzer *Analyzer) Status(ctx context.Context, reset, recheck bool) map[string]any {
	if ctx == nil {
		ctx = context.Background()
	}
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	if reset {
		analyzer.checked = false
		analyzer.installed = false
		analyzer.ffmpeg = ""
		analyzer.ffprobe = ""
		analyzer.availableEncoders = ""
	}
	if !analyzer.checked || recheck {
		_ = analyzer.verifyInstalledLocked(ctx)
		analyzer.checked = true
	}
	var which any
	if analyzer.ffmpeg != "" {
		which = analyzer.ffmpeg
	}
	return map[string]any{
		"available":            analyzer.installed,
		"which":                which,
		"analyze_audio_volume": analyzer.config().VolumeAnalysisTime > 0,
	}
}

func (analyzer *Analyzer) verifyInstalledLocked(ctx context.Context) error {
	config := analyzer.config()
	searchPath := config.FFmpegPath
	if config.DataDir != "" {
		searchPath = appendSearchPath(searchPath, filepath.Join(config.DataDir, "ffmpeg", "bin"))
	}
	searchPath = appendSearchPath(searchPath, os.Getenv("PATH"))
	analyzer.ffmpeg = lookPathIn("ffmpeg", searchPath)
	analyzer.ffprobe = lookPathIn("ffprobe", searchPath)
	analyzer.installed = false
	if analyzer.ffmpeg == "" || analyzer.ffprobe == "" {
		return errors.New(unavailableMessage)
	}
	if filepath.Dir(analyzer.ffmpeg) != filepath.Dir(analyzer.ffprobe) {
		analyzer.logf("FFmpeg: ffmpeg and ffprobe were found in different directories.")
	}
	_, _, _ = analyzer.run(ctx, analyzer.ffprobe, "-version")
	version, code, err := analyzer.run(ctx, analyzer.ffmpeg, "-version")
	if err != nil || code != 0 || !strings.HasPrefix(version, "ffmpeg") {
		return errors.New(unavailableMessage)
	}
	analyzer.installed = true
	versionLine := strings.SplitN(version, "\n", 2)[0]
	if before, _, found := strings.Cut(versionLine, " Copyright"); found {
		versionLine = before
	}
	analyzer.logf("FFmpeg: using %s at %s.", versionLine, analyzer.ffmpeg)
	return nil
}

func (analyzer *Analyzer) ensureInstalled(ctx context.Context) (Config, string, string, error) {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	if !analyzer.installed {
		if err := analyzer.verifyInstalledLocked(ctx); err != nil {
			return analyzer.config(), analyzer.ffmpeg, analyzer.ffprobe, err
		}
		analyzer.checked = true
	}
	return analyzer.config(), analyzer.ffmpeg, analyzer.ffprobe, nil
}

type probeFormat struct {
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
	Duration       any    `json:"duration"`
	BitRate        any    `json:"bit_rate"`
}

type probeStream struct {
	CodecType     string `json:"codec_type"`
	CodecName     string `json:"codec_name"`
	CodecLongName string `json:"codec_long_name"`
	PixelFormat   string `json:"pix_fmt"`
	SampleRate    any    `json:"sample_rate"`
	Width         any    `json:"width"`
	Height        any    `json:"height"`
}

type probeData struct {
	Format  probeFormat   `json:"format"`
	Streams []probeStream `json:"streams"`
}

func decodeProbeData(raw string, filePath string) (*probeData, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var scan probeData
	if err := decoder.Decode(&scan); err != nil {
		return nil, fmt.Errorf("Absent or unreadable video file: %s", filePath)
	}
	duration, err := numericFloat(scan.Format.Duration)
	if err != nil {
		return nil, fmt.Errorf("Media file does not appear to contain video content: %s", filePath)
	}
	if duration < 0.1 {
		return nil, fmt.Errorf("Assuming image file at: %s", filePath)
	}
	return &scan, nil
}

func numericFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Float64()
	case string:
		return strconv.ParseFloat(typed, 64)
	case float64:
		return typed, nil
	case int:
		return float64(typed), nil
	default:
		return 0, fmt.Errorf("invalid numeric value %v", value)
	}
}

func numericInt(value any) (int, error) {
	number, err := numericFloat(value)
	if err != nil || number != math.Trunc(number) {
		return 0, fmt.Errorf("invalid integer value %v", value)
	}
	return int(number), nil
}

func names(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		result[item] = struct{}{}
	}
	return result
}

func intersects(values map[string]struct{}, candidates ...string) bool {
	for _, candidate := range candidates {
		if _, ok := values[candidate]; ok {
			return true
		}
	}
	return false
}

func verifyContainer(scan *probeData) string {
	containers := names(scan.Format.FormatName)
	if !intersects(containers, "webm", "mp4", "3gp", "ogg") {
		return fmt.Sprintf(
			"Container format is not in the approved list of WebM, MP4. Actual: %s [%s]",
			scan.Format.FormatName, scan.Format.FormatLongName,
		)
	}
	if _, matroska := containers["matroska"]; matroska {
		for _, stream := range scan.Streams {
			codecs := names(stream.CodecName)
			switch stream.CodecType {
			case "video":
				if !intersects(codecs, "vp8", "vp9", "av1") {
					return fmt.Sprintf("WebM format requires VP8/9 or AV1 video. Actual: %s [%s]", stream.CodecName, stream.CodecLongName)
				}
			case "audio":
				if !intersects(codecs, "vorbis", "opus") {
					return fmt.Sprintf("WebM format requires Vorbis or Opus audio. Actual: %s [%s]", stream.CodecName, stream.CodecLongName)
				}
			}
		}
	}
	return ""
}

func verifyVideoEncoding(scan *probeData) string {
	for _, stream := range scan.Streams {
		if stream.CodecType != "video" {
			continue
		}
		codecs := names(stream.CodecName)
		if !intersects(codecs, "h264", "vp8", "vp9", "av1", "theora") {
			return fmt.Sprintf(
				"Video codec is not in the approved list of H264, VP8, VP9, AV1, Theora. Actual: %s [%s]",
				stream.CodecName, stream.CodecLongName,
			)
		}
		if _, h264 := codecs["h264"]; h264 && stream.PixelFormat != "yuv420p" {
			return fmt.Sprintf(
				"Video codec is H264, but its pixel format does not match the approved yuv420p. Actual: %s",
				stream.PixelFormat,
			)
		}
	}
	return ""
}

func verifyAudioEncoding(scan *probeData) string {
	for _, stream := range scan.Streams {
		if stream.CodecType != "audio" {
			continue
		}
		if !intersects(names(stream.CodecName), "aac", "mp3", "flac", "vorbis", "opus") {
			return fmt.Sprintf(
				"Audio codec is not in the approved list of AAC, FLAC, MP3, Vorbis, and Opus. Actual: %s [%s]",
				stream.CodecName, stream.CodecLongName,
			)
		}
		rate, err := numericInt(stream.SampleRate)
		if err == nil && rate > 48_000 {
			return "Sample rate out of range"
		}
	}
	return ""
}

func verifyBitrate(scan *probeData, filePath string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	bitrate, err := numericFloat(scan.Format.BitRate)
	if err != nil {
		duration, durationErr := numericFloat(scan.Format.Duration)
		info, statErr := os.Stat(filePath)
		if durationErr != nil || statErr != nil || duration <= 0 {
			return ""
		}
		bitrate = float64(info.Size()) / duration
	}
	if bitrate > float64(maximum) {
		return fmt.Sprintf(
			"The bit rate is above the configured maximum. Actual: %v Mbps; Allowed max: %v Mbps",
			bitrate/1_000_000.0, float64(maximum)/1_000_000.0,
		)
	}
	return ""
}

func buildSpec(scan *probeData) (map[string]any, error) {
	duration, err := numericFloat(scan.Format.Duration)
	if err != nil {
		return nil, err
	}
	spec := map[string]any{"duration": int(math.Ceil(duration))}
	width, height := -1, -1
	for _, stream := range scan.Streams {
		if stream.CodecType != "video" {
			continue
		}
		if value, valueErr := numericInt(stream.Width); valueErr == nil {
			width = max(width, value)
		}
		if value, valueErr := numericInt(stream.Height); valueErr == nil {
			height = max(height, value)
		}
	}
	if width >= 0 {
		spec["width"] = width
	}
	if height >= 0 {
		spec["height"] = height
	}
	return spec, nil
}

func (analyzer *Analyzer) scan(ctx context.Context, ffprobe, filePath string) (*probeData, error) {
	result, _, _ := analyzer.run(ctx, ffprobe,
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", filePath,
	)
	return decodeProbeData(result, filePath)
}

func (analyzer *Analyzer) verifyFastStart(ctx context.Context, ffprobe, filePath string, scan *probeData) string {
	containers := names(scan.Format.FormatName)
	if intersects(containers, "webm", "ogg") {
		return ""
	}
	result, _, _ := analyzer.run(ctx, ffprobe, "-v", "debug", filePath)
	match := fastStartPattern.FindStringSubmatch(result)
	if len(match) == 2 && match[1] != "0" {
		return "Video stream descriptors are not at the start of the file (the faststart flag was not used)."
	}
	return ""
}

func (analyzer *Analyzer) verifyAudioVolume(ctx context.Context, ffmpeg, filePath string, seconds int) string {
	if seconds <= 0 {
		return ""
	}
	result, _, _ := analyzer.run(ctx, ffmpeg,
		"-i", filePath, "-t", strconv.Itoa(seconds), "-af", "volumedetect",
		"-vn", "-sn", "-dn", "-f", "null", os.DevNull,
	)
	meanMatch, maxMatch := meanVolumePattern.FindStringSubmatch(result), maxVolumePattern.FindStringSubmatch(result)
	if len(meanMatch) != 2 || len(maxMatch) != 2 {
		return ""
	}
	meanVolume, meanErr := strconv.ParseFloat(meanMatch[1], 64)
	maxVolume, maxErr := strconv.ParseFloat(maxMatch[1], 64)
	if meanErr != nil || maxErr != nil {
		return ""
	}
	if maxVolume < -5.0 && meanVolume < -22.0 {
		return fmt.Sprintf("Audio is at least five dB lower than prime. Actual max: %v, mean: %v", maxVolume, meanVolume)
	}
	return ""
}

// VerifyOrRepair validates browser streamability and optionally writes a
// repaired sibling file, matching the pinned SDK's publication path.
func (analyzer *Analyzer) VerifyOrRepair(
	ctx context.Context, validate, repair bool, filePath string, ignoreNonVideo bool,
) (string, map[string]any, error) {
	if !validate && !repair {
		return filePath, map[string]any{}, nil
	}
	if ignoreNonVideo && filePath == "" {
		return filePath, map[string]any{}, nil
	}
	config, ffmpeg, ffprobe, err := analyzer.ensureInstalled(ctx)
	if err != nil {
		return filePath, nil, err
	}
	scan, err := analyzer.scan(ctx, ffprobe, filePath)
	if err != nil {
		if ignoreNonVideo {
			return filePath, map[string]any{}, nil
		}
		return filePath, nil, err
	}
	spec, err := buildSpec(scan)
	if err != nil {
		return filePath, nil, err
	}
	analyzer.logf("FFmpeg: analyzing %s.", filePath)
	containerMessage := verifyContainer(scan)
	bitrateMessage := verifyBitrate(scan, filePath, config.VideoBitrateMax)
	fastStartMessage := analyzer.verifyFastStart(ctx, ffprobe, filePath, scan)
	videoMessage := verifyVideoEncoding(scan)
	audioMessage := verifyAudioEncoding(scan)
	volumeMessage := analyzer.verifyAudioVolume(ctx, ffmpeg, filePath, config.VolumeAnalysisTime)
	messages := []string{containerMessage, bitrateMessage, fastStartMessage, videoMessage, audioMessage, volumeMessage}
	invalid := false
	for _, message := range messages {
		invalid = invalid || message != ""
	}
	if !invalid {
		analyzer.logf("FFmpeg: %s is browser-compatible.", filePath)
		return filePath, spec, nil
	}
	if !repair {
		problems := []string{"Streamability verification failed:"}
		for _, message := range messages {
			if message != "" {
				problems = append(problems, message)
			}
		}
		return filePath, spec, errors.New(strings.Join(problems, "\n   "))
	}
	output, transcodeErr := analyzer.transcode(
		ctx, config, ffmpeg, filePath, scan,
		videoMessage, bitrateMessage, audioMessage, volumeMessage,
	)
	if transcodeErr != nil {
		if validate {
			return filePath, spec, transcodeErr
		}
		analyzer.logf("FFmpeg: unable to optimize %s; publishing the original: %v", filePath, transcodeErr)
		return filePath, spec, nil
	}
	analyzer.logf("FFmpeg: optimized %s to %s.", filePath, output)
	return output, spec, nil
}

func (analyzer *Analyzer) encoderList(ctx context.Context, ffmpeg string) string {
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	if analyzer.availableEncoders == "" {
		analyzer.availableEncoders, _, _ = analyzer.run(ctx, ffmpeg, "-encoders", "-v", "quiet")
	}
	return analyzer.availableEncoders
}

func encoderAvailable(listing, kind, name string) bool {
	if name == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(kind) + `..... ` + regexp.QuoteMeta(name) + ` `)
	return pattern.MatchString(listing)
}

func commandName(command string) string {
	parts, _ := splitCommand(command)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func (analyzer *Analyzer) videoEncoder(ctx context.Context, config Config, ffmpeg string, scan *probeData) (string, error) {
	listing := analyzer.encoderList(ctx, ffmpeg)
	requested := commandName(config.VideoEncoder)
	if encoderAvailable(listing, "V", requested) {
		return config.VideoEncoder, nil
	}
	if encoderAvailable(listing, "V", "libx264") {
		return `libx264 -crf 19 -vf "format=yuv420p"`, nil
	}
	if requested == "" {
		requested = "libx264"
	}
	if encoderAvailable(listing, "V", "libvpx-vp9") {
		height := 240.0
		for _, stream := range scan.Streams {
			if stream.CodecType == "video" {
				if value, err := numericFloat(stream.Height); err == nil {
					height = max(height, value)
				}
			}
		}
		return fmt.Sprintf("libvpx-vp9 -crf %d -b:v 0", int(-0.011*height+40)), nil
	}
	if encoderAvailable(listing, "V", "libtheora") {
		return "libtheora -q:v 7", nil
	}
	return "", fmt.Errorf("The video encoder is not available. Requested: %s", requested)
}

func (analyzer *Analyzer) audioEncoder(ctx context.Context, config Config, ffmpeg, extension string) (string, error) {
	listing := analyzer.encoderList(ctx, ffmpeg)
	requested := commandName(config.AudioEncoder)
	wantsOpus := extension != "mp4"
	if wantsOpus && strings.Contains(requested, "opus") {
		return config.AudioEncoder, nil
	}
	if wantsOpus && encoderAvailable(listing, "A", "libopus") {
		return "libopus -b:a 160k", nil
	}
	if wantsOpus && strings.Contains(requested, "vorbis") {
		return config.AudioEncoder, nil
	}
	if wantsOpus && encoderAvailable(listing, "A", "libvorbis") {
		return "libvorbis -q:a 6", nil
	}
	if encoderAvailable(listing, "A", requested) {
		return config.AudioEncoder, nil
	}
	if encoderAvailable(listing, "A", "aac") {
		return "aac -b:a 192k", nil
	}
	if requested == "" {
		requested = "aac"
	}
	return "", fmt.Errorf("The audio encoder is not available. Requested: %s", requested)
}

func bestExtension(scan *probeData, videoEncoder string) string {
	if videoEncoder != "" {
		if strings.Contains(videoEncoder, "theora") {
			return "ogv"
		}
		name := commandName(videoEncoder)
		if regexp.MustCompile(`vp[89x]|av1`).MatchString(name) {
			return "webm"
		}
		return "mp4"
	}
	for _, stream := range scan.Streams {
		if stream.CodecType != "video" {
			continue
		}
		codecs := names(stream.CodecName)
		if _, ok := codecs["theora"]; ok {
			return "ogv"
		}
		if intersects(codecs, "vp8", "vp9", "av1") {
			return "webm"
		}
	}
	return "mp4"
}

func appendCommand(arguments []string, command string) ([]string, error) {
	parts, err := splitCommand(command)
	if err != nil {
		return nil, err
	}
	return append(arguments, parts...), nil
}

func (analyzer *Analyzer) transcode(
	ctx context.Context, config Config, ffmpeg, filePath string, scan *probeData,
	videoMessage, bitrateMessage, audioMessage, volumeMessage string,
) (string, error) {
	arguments := []string{"-i", filePath, "-y", "-c:s", "copy", "-c:d", "copy", "-c:v"}
	videoEncoder := ""
	var err error
	if videoMessage != "" || bitrateMessage != "" {
		videoEncoder, err = analyzer.videoEncoder(ctx, config, ffmpeg, scan)
		if err != nil {
			return "", err
		}
		arguments, err = appendCommand(arguments, videoEncoder)
		if err != nil {
			return "", err
		}
		arguments, err = appendCommand(arguments, config.VideoScaler)
		if err != nil {
			return "", err
		}
	} else {
		arguments = append(arguments, "copy")
	}
	arguments = append(arguments, "-movflags", "+faststart", "-c:a")
	extension := bestExtension(scan, videoEncoder)
	if audioMessage != "" || volumeMessage != "" {
		audioEncoder, encoderErr := analyzer.audioEncoder(ctx, config, ffmpeg, extension)
		if encoderErr != nil {
			return "", encoderErr
		}
		arguments, err = appendCommand(arguments, audioEncoder)
		if err != nil {
			return "", err
		}
		if volumeMessage != "" && config.VolumeFilter != "" {
			arguments, err = appendCommand(arguments, config.VolumeFilter)
			if err != nil {
				return "", err
			}
		}
		if audioMessage == "Sample rate out of range" {
			arguments = append(arguments, "-ar", "48000")
		}
	} else {
		arguments = append(arguments, "copy")
	}
	extensionless := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	output := extensionless + "_fixed." + extension
	arguments = append(arguments, output)
	analyzer.logf("FFmpeg: optimizing %s.", filePath)
	result, code, runErr := analyzer.run(ctx, ffmpeg, arguments...)
	if runErr != nil || code != 0 {
		return "", fmt.Errorf("Failure to complete the transcode command. Output: %s", result)
	}
	return output, nil
}

func splitCommand(command string) ([]string, error) {
	var arguments []string
	var current bytes.Buffer
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			arguments = append(arguments, current.String())
			current.Reset()
		}
	}
	for _, value := range command {
		if escaped {
			if quote == '"' && value != '"' && value != '\\' && value != '$' && value != '`' {
				current.WriteRune('\\')
			}
			current.WriteRune(value)
			escaped = false
			continue
		}
		if value == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			} else {
				current.WriteRune(value)
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if unicode.IsSpace(value) {
			flush()
			continue
		}
		current.WriteRune(value)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errors.New("unterminated quoted FFmpeg configuration")
	}
	flush()
	return arguments, nil
}

func appendSearchPath(current, addition string) string {
	if current == "" {
		return addition
	}
	if addition == "" {
		return current
	}
	return current + string(os.PathListSeparator) + addition
}

func lookPathIn(name, searchPath string) string {
	for _, directory := range filepath.SplitList(searchPath) {
		if directory == "" {
			continue
		}
		candidate, err := exec.LookPath(filepath.Join(directory, name))
		if err == nil {
			return candidate
		}
	}
	return ""
}
