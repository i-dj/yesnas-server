package thumbnail

import (
	"crypto/md5"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/disintegration/imaging"
)

// 💡 这里的常量你可以根据需要微调
const (
	DefaultMaxSize  = 400                 // 建议 300，兼顾清晰度和加载速度
	DefaultQuality  = 75                  // 建议 60-75
	DefaultCacheDir = "./data/thumbnails" // 建议改到项目目录下，方便观察
)

// 限制并发处理数，防止大图解码占用过多内存
var (
	concurrencyLimit = make(chan struct{}, runtime.NumCPU())

	// 支持的图片后缀
	imgExtensions = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true, ".webp": true,
	}

	// 支持的视频后缀 (需要系统安装了 ffmpeg)
	videoExtensions = map[string]bool{
		".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true, ".flv": true,
	}
)

// GetCachePath 根据文件路径生成唯一缓存路径
func GetCachePath(filePath, cacheDir string) string {
	hash := fmt.Sprintf("%x", md5.Sum([]byte(filePath)))
	return filepath.Join(cacheDir, hash+".jpg")
}

// GenerateImageThumbnail 处理图片缩放
func GenerateImageThumbnail(src, dst string) error {
	concurrencyLimit <- struct{}{}
	defer func() { <-concurrencyLimit }()

	img, err := imaging.Open(src)
	if err != nil {
		return err
	}

	// 使用 Box 算法，在大图缩小时性能最好
	dstImg := imaging.Fit(img, DefaultMaxSize, DefaultMaxSize, imaging.Box)
	return imaging.Save(dstImg, dst, imaging.JPEGQuality(DefaultQuality))
}

// GenerateVideoThumbnail 使用 ffmpeg 抽帧并缩放
func GenerateVideoThumbnail(src, dst string) error {
	concurrencyLimit <- struct{}{}
	defer func() { <-concurrencyLimit }()

	// 1. 先抽取一帧作为临时文件
	tmpJpeg := dst + ".tmp.jpg"
	// -ss 1: 跳到第1秒; -vframes 1: 只截取1帧
	cmd := exec.Command("ffmpeg", "-y", "-i", src, "-ss", "1", "-vframes", "1", "-f", "image2", tmpJpeg)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg error: %w", err)
	}
	defer os.Remove(tmpJpeg) // 任务结束删除临时文件

	// 2. 对抽取的帧进行缩放优化
	img, err := imaging.Open(tmpJpeg)
	if err != nil {
		return err
	}
	dstImg := imaging.Fit(img, DefaultMaxSize, DefaultMaxSize, imaging.Box)
	return imaging.Save(dstImg, dst, imaging.JPEGQuality(DefaultQuality))
}

// GetOrCreateThumbnail 外部调用的主入口
func GetOrCreateThumbnail(filePath, cacheDir string) (string, error) {
	finalCacheDir := cacheDir
	if finalCacheDir == "" {
		finalCacheDir = DefaultCacheDir
	}

	// 1. 检查后缀支持情况
	ext := strings.ToLower(filepath.Ext(filePath))
	isImg := imgExtensions[ext]
	isVideo := videoExtensions[ext]

	if !isImg && !isVideo {
		return "", fmt.Errorf("格式不支持: %s", ext)
	}

	// 2. 检查缓存
	cachePath := GetCachePath(filePath, finalCacheDir)
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	// 3. 确保目录存在
	if err := os.MkdirAll(finalCacheDir, 0755); err != nil {
		return "", err
	}

	// 4. 生成
	log.Printf("[THUMBNAIL] 正在生成: %s", filepath.Base(filePath))
	var err error
	if isVideo {
		err = GenerateVideoThumbnail(filePath, cachePath)
	} else {
		err = GenerateImageThumbnail(filePath, cachePath)
	}

	if err != nil {
		return "", err
	}
	return cachePath, nil
}
