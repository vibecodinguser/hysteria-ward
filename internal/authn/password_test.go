package authn_test

import (
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
)

// TestHashAndVerify — пароль проходит проверку, а всё остальное на него не похожее
// — нет. Неподходящий пароль при этом не ошибка: о нём молчат, а не жалуются.
func TestHashAndVerify(t *testing.T) {
	const password = "пароль администратора"

	encoded, err := authn.Hash(password)
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	matched, err := authn.Verify(encoded, password)
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("свой пароль не подошёл")
	}

	matched, err = authn.Verify(encoded, password+"!")
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if matched {
		t.Error("чужой пароль подошёл")
	}
}

// TestHashUsesFreshSalt — два хеша одного пароля не совпадают: по базе нельзя
// понять, у кого пароли одинаковые.
func TestHashUsesFreshSalt(t *testing.T) {
	first, err := authn.Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	second, err := authn.Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	if first == second {
		t.Error("хеши одного пароля совпали: соль не случайна")
	}
}

// TestHashFormat — хеш записан в общем формате Argon2: параметры и соль лежат в
// самой строке, поэтому смена параметров не ломает уже записанные пароли.
func TestHashFormat(t *testing.T) {
	encoded, err := authn.Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=1,p=2$") {
		t.Errorf("хеш = %q, ожидался argon2id с параметрами", encoded)
	}
}

// TestGeneratePassword — придуманный пароль не пустой, не повторяется и годится
// для входа. 24 символа base64url: перебрать нельзя, скопировать можно.
func TestGeneratePassword(t *testing.T) {
	first, err := authn.GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword() = %v", err)
	}
	second, err := authn.GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword() = %v", err)
	}

	if len(first) != 24 {
		t.Errorf("длина пароля = %d, ожидалось 24", len(first))
	}
	if first == second {
		t.Error("два сгенерированных пароля совпали")
	}

	encoded, err := authn.Hash(first)
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}
	matched, err := authn.Verify(encoded, first)
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("сгенерированный пароль не подошёл к своему хешу")
	}
}

// TestVerifyRejectsBrokenHash — испорченный хеш отличается от неверного пароля:
// это поломка базы, и о ней нужно сказать, а не молча никого не пустить.
func TestVerifyRejectsBrokenHash(t *testing.T) {
	for _, encoded := range []string{
		"",
		"пароль",
		"$argon2i$v=19$m=65536,t=1,p=2$соль$ключ",
		"$argon2id$v=19$m=65536,t=1,p=2$нет-такого-ключа$",
		"$argon2id$v=19$m=не-число$соль$ключ",
		"$argon2id$v=18$m=65536,t=1,p=2$соль$ключ",
	} {
		if _, err := authn.Verify(encoded, "пароль"); err == nil {
			t.Errorf("Verify(%q) прошёл, ожидалась ошибка", encoded)
		}
	}
}
