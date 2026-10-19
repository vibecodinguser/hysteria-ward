package store_test

import (
	"testing"
)

// TestDomainIsNotSetAtFirst — домена нет, и это видно: ключ клиента без адреса не
// собрать, панель обязана сказать об этом, а не выдать пустую ссылку.
func TestDomainIsNotSetAtFirst(t *testing.T) {
	s := openStore(t)

	domain, err := s.Domain()
	if err != nil {
		t.Fatalf("Domain() = %v", err)
	}
	if domain != "" {
		t.Errorf("домен = %q, ожидалась пустая строка", domain)
	}
}

// TestDomainRoundTrip — домен нормализуется при записи и переживает перезапуск:
// его читают ключи и, позже, выпуск сертификата.
func TestDomainRoundTrip(t *testing.T) {
	s := openStore(t)

	if err := s.SaveDomain("  Example.COM \n"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}

	domain, err := s.Domain()
	if err != nil {
		t.Fatalf("Domain() = %v", err)
	}
	if domain != "example.com" {
		t.Errorf("домен = %q, ожидался %q", domain, "example.com")
	}

	if err := s.SaveDomain(""); err != nil {
		t.Fatalf("SaveDomain(\"\") = %v", err)
	}

	if domain, err := s.Domain(); err != nil || domain != "" {
		t.Errorf("после стирания домен = %q, err %v", domain, err)
	}
}

// TestSaveDomainAcceptsIP — сервер может стоять на адресе без имени: ключ с IP
// работать будет, хотя сертификат по HTTP-01 на него не выпустить.
func TestSaveDomainAcceptsIP(t *testing.T) {
	s := openStore(t)

	if err := s.SaveDomain("203.0.113.5"); err != nil {
		t.Errorf("SaveDomain(IP) = %v", err)
	}
}

// TestSaveDomainRejectsNonsense — то, что не является именем, отвергается с
// объяснением: иначе в ключ попадёт адрес, по которому никто не подключится.
func TestSaveDomainRejectsNonsense(t *testing.T) {
	s := openStore(t)

	for _, domain := range []string{
		"https://example.com",
		"example.com/panel",
		"example.com:443",
		"exa mple.com",
		"example..com",
		"-example.com",
		"example-.com",
		"example.com?x=1",
	} {
		if err := s.SaveDomain(domain); err == nil {
			t.Errorf("SaveDomain(%q) = nil, ожидалась ошибка", domain)
		}
	}
}
