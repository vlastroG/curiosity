// Package docs -- индекс документации продукта: обход файлов, нарезка на чанки,
// векторы bge-m3 и BM25, гибридный поиск.
package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// maxFileSize -- файлы крупнее (сгенерированные модули, дампы) не индексируются.
const maxFileSize = 512 << 10

// Options -- что и чем индексировать.
type Options struct {
	Root    string
	Include []string // расширения с точкой: .md, .py
	Exclude []string // имена каталогов, которые пропускаются целиком
	Model   string
	// Label -- как назвать каталог в ошибках; в Docker Root -- /docs, а пользователь задавал путь на хосте
	Label string
}

// File -- файл документации.
type File struct {
	Path    string // относительный, через /
	Size    int64
	ModTime time.Time
}

// Walk -- подходящие файлы под Root, по возрастанию пути.
func Walk(o Options) ([]File, error) {
	label := o.Label
	if label == "" {
		label = o.Root
	}
	info, err := os.Stat(o.Root)
	if err != nil {
		return nil, fmt.Errorf("каталог документации %s недоступен: %w", label, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s -- не каталог", label)
	}
	var files []File
	err = filepath.WalkDir(o.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != o.Root && slices.Contains(o.Exclude, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !slices.Contains(o.Include, strings.ToLower(filepath.Ext(path))) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 || info.Size() > maxFileSize {
			return nil
		}
		rel, err := filepath.Rel(o.Root, path)
		if err != nil {
			return err
		}
		files = append(files, File{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("в %s нет подходящих файлов", label)
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// Fingerprint -- отпечаток набора файлов: меняется, если поменялся хоть один
// файл, модель эмбеддингов или нарезка.
func Fingerprint(model string, files []File) string {
	h := sha256.New()
	fmt.Fprintf(h, "chunker %s\nmodel %s\n", chunkerVersion, model)
	for _, f := range files {
		fmt.Fprintf(h, "%s|%d|%d\n", f.Path, f.Size, f.ModTime.Unix())
	}
	return hex.EncodeToString(h.Sum(nil))
}
