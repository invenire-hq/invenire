// Package password guarda e confere senhas de login do painel com argon2id.
//
// O hash fica no formato PHC ($argon2id$v=19$m=65536,t=3,p=2$sal$hash), com
// os parâmetros dentro dele: mudar os parâmetros daqui não invalida as senhas
// já guardadas.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Parâmetros do argon2id (RFC 9106, com memória de 64 MiB).
const (
	memoryKiB = 64 * 1024
	timeCost  = 3
	threads   = 2
	saltLen   = 16
	keyLen    = 32

	// MinLength é o tamanho mínimo de uma senha nova, em caracteres.
	MinLength = 10
	maxLength = 1024
)

// Cada hash usa 64 MiB: no máximo quatro ao mesmo tempo, para uma rajada de
// logins não esgotar a memória do servidor.
var slots = make(chan struct{}, 4)

func derive(pw string, salt []byte, m, t uint32, p uint8, n uint32) []byte {
	slots <- struct{}{}
	defer func() { <-slots }()
	return argon2.IDKey([]byte(pw), salt, t, m, p, n)
}

// Validate confere uma senha nova.
func Validate(pw string) error {
	switch n := utf8.RuneCountInString(pw); {
	case n < MinLength:
		return fmt.Errorf("a senha precisa de pelo menos %d caracteres", MinLength)
	case len(pw) > maxLength:
		return errors.New("senha longa demais")
	case strings.TrimSpace(pw) == "":
		return errors.New("senha só de espaços")
	}
	return nil
}

// Hash devolve o hash PHC da senha.
func Hash(pw string) (string, error) {
	if err := Validate(pw); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key := derive(pw, salt, memoryKiB, timeCost, threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memoryKiB, timeCost, threads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify diz se pw confere com o hash. Um hash ilegível é erro, não "não
// confere".
func Verify(hash, pw string) (bool, error) {
	if len(pw) > maxLength {
		return false, nil
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("hash de senha em formato desconhecido")
	}
	var v int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errors.New("versão do argon2 desconhecida")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<22 {
		return false, errors.New("parâmetros do argon2 inválidos")
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("hash de senha inválido")
	}
	got := derive(pw, salt, m, t, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummy é um hash qualquer, para o login de um e-mail que não existe levar o
// mesmo tempo que o de um que existe.
var dummy = sync.OnceValue(func() string {
	b := make([]byte, 18)
	rand.Read(b)
	h, _ := Hash(base64.RawStdEncoding.EncodeToString(b))
	return h
})

// Waste gasta o mesmo tempo de um Verify, sem conferir nada.
func Waste(pw string) { Verify(dummy(), pw) }
