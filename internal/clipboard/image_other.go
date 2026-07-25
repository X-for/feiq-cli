//go:build !darwin

package clipboard

import "fmt"

func SavePNG(path string) error {
	return fmt.Errorf("当前系统暂不支持直接读取剪贴板图片；请将图片保存为文件后使用 /send file")
}
