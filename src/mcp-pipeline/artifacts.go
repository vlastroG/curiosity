package main

// Артефакты -- то, как данные переходят от инструмента к инструменту.
//
// Инструменты обмениваются не текстом, а хендлами. hn_search сохраняет выдачу
// и возвращает её id; summarize получает id, а не саму выдачу; save_to_file --
// id конспекта. Модели, которая строит цепочку, не нужно переписывать сотни строк
// комментариев из одного вызова в другой -- значит, по дороге ничего не потеряется
// и не будет выдумано.
//
// У каждого артефакта есть sha256 содержимого. Следующий шаг сверяет его: конспект
// помнит хеш выдачи, по которой сделан, и сохранить конспект к подменённой выдаче
// не получится.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Типы артефактов и префиксы их id.
const (
	KindSearch  = "search"
	KindSummary = "summary"
)

var prefixes = map[string]string{KindSearch: "src", KindSummary: "sum"}

// idPattern -- как выглядит id. Проверка до обращения к диску: id приходит от
// модели, и превратиться в путь вида ../../что-то он не должен.
var idPattern = regexp.MustCompile(`^(src|sum)_[0-9a-f]{10}$`)

// Artifact -- сохранённый результат шага.
type Artifact struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	CreatedAt string          `json:"createdAt"`
	SHA256    string          `json:"sha256"`
	Payload   json.RawMessage `json:"payload"`
}

// ErrNotFound -- такого артефакта нет.
var ErrNotFound = errors.New("артефакт не найден")

// Artifacts -- хранилище в каталоге на диске.
type Artifacts struct {
	dir string
	now func() time.Time
}

func newArtifacts(dir string) (*Artifacts, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Artifacts{dir: dir, now: time.Now}, nil
}

// Put сохраняет payload и возвращает артефакт с id и хешем.
func (a *Artifacts) Put(kind string, payload any) (Artifact, error) {
	prefix, ok := prefixes[kind]
	if !ok {
		return Artifact{}, fmt.Errorf("неизвестный тип артефакта %q", kind)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Artifact{}, err
	}

	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return Artifact{}, err
	}
	art := Artifact{
		ID:        prefix + "_" + hex.EncodeToString(random),
		Kind:      kind,
		CreatedAt: a.now().UTC().Format(time.RFC3339),
		SHA256:    digest(raw),
		Payload:   raw,
	}

	file, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return Artifact{}, err
	}
	// сначала во временный файл, потом переименование: полузаписанный артефакт
	// после сбоя хуже, чем никакого
	path := filepath.Join(a.dir, art.ID+".json")
	if err := os.WriteFile(path+".tmp", file, 0o644); err != nil {
		return Artifact{}, err
	}
	return art, os.Rename(path+".tmp", path)
}

// Get читает артефакт нужного типа и сверяет хеш содержимого.
func (a *Artifacts) Get(id, kind string) (Artifact, error) {
	if !idPattern.MatchString(id) {
		return Artifact{}, fmt.Errorf("%w: %q не похоже на id артефакта", ErrNotFound, id)
	}
	raw, err := os.ReadFile(filepath.Join(a.dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Artifact{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Artifact{}, err
	}

	var art Artifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return Artifact{}, fmt.Errorf("артефакт %s повреждён: %w", id, err)
	}
	if art.Kind != kind {
		return Artifact{}, fmt.Errorf("%s -- это артефакт типа %q, а нужен %q", id, art.Kind, kind)
	}
	// на диске payload лежит с отступами; хеш считается по компактной форме
	var compact bytes.Buffer
	if err := json.Compact(&compact, art.Payload); err != nil {
		return Artifact{}, fmt.Errorf("артефакт %s повреждён: %w", id, err)
	}
	if got := digest(compact.Bytes()); got != art.SHA256 {
		return Artifact{}, fmt.Errorf("содержимое %s не совпадает с хешем: записан %s, получен %s", id, shortHash(art.SHA256), shortHash(got))
	}
	art.Payload = compact.Bytes()
	return art, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// shortHash -- первые 12 символов хеша для логов и текста модели.
func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
