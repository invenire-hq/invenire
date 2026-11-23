// SPDX-License-Identifier: AGPL-3.0-only

package password

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := Hash("uma frase longa de teste")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("formato: %s", h)
	}
	if ok, err := Verify(h, "uma frase longa de teste"); !ok || err != nil {
		t.Fatalf("senha certa: %v %v", ok, err)
	}
	if ok, _ := Verify(h, "uma frase longa de testE"); ok {
		t.Fatal("senha errada conferiu")
	}
	h2, _ := Hash("uma frase longa de teste")
	if h2 == h {
		t.Fatal("dois hashes da mesma senha deveriam ter sal diferente")
	}
	if _, err := Hash("curta"); err == nil {
		t.Fatal("senha curta aceita")
	}
	if _, err := Verify("$bcrypt$x", "x"); err == nil {
		t.Fatal("hash desconhecido deveria ser erro")
	}
}
