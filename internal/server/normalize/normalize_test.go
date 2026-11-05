package normalize

import (
	"testing"
)

func TestEvaluate(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		source, name, vendor string
		want                 Match // Confidence 0 = não reconhece
	}{
		// Catálogo: mesmo produto em fontes diferentes.
		{"win_registry", "Google Chrome", "Google LLC", Match{"Google", "Chrome", 1, "catalog:chrome"}},
		{"dpkg", "google-chrome-stable", "", Match{"Google", "Chrome", 1, "catalog:chrome"}},
		{"rpm", "google-chrome-stable", "", Match{"Google", "Chrome", 1, "catalog:chrome"}},
		{"win_registry", "Mozilla Firefox (x64 en-US)", "Mozilla", Match{"Mozilla", "Firefox", 1, "catalog:firefox"}},
		{"win_registry", "Mozilla Firefox ESR (x64 pt-BR)", "Mozilla", Match{"Mozilla", "Firefox ESR", 1, "catalog:firefox-esr"}},
		{"win_registry", "7-Zip 26.04 (x64)", "Igor Pavlov", Match{"Igor Pavlov", "7-Zip", 1, "catalog:7zip"}},
		{"win_registry", "Notepad++ (64-bit x64)", "Notepad++ Team", Match{"Notepad++", "Notepad++", 1, "catalog:notepadpp"}},
		{"win_registry", "Microsoft Visual C++ 2015-2022 Redistributable (x64) - 14.44.35211", "Microsoft Corporation", Match{"Microsoft", "Visual C++ Redistributable", 1, "catalog:vcredist"}},
		{"win_registry", "Microsoft Visual C++ 2022 X86 Minimum Runtime - 14.44.35211", "Microsoft Corporation", Match{"Microsoft", "Visual C++ Redistributable", 1, "catalog:vcredist"}},
		{"win_registry", "Visual Studio Community 2022", "Microsoft Corporation", Match{"Microsoft", "Visual Studio Community 2022", 1, "catalog:visual-studio"}},
		{"win_registry", "Python 3.11.9 Core Interpreter (64-bit)", "Python Software Foundation", Match{"Python Software Foundation", "Python", 1, "catalog:python"}},
		{"win_registry", "Python 3.11.9 (64-bit)", "Python Software Foundation", Match{"Python Software Foundation", "Python", 1, "catalog:python"}},
		{"win_registry", "Java 8 Update 481 (64-bit)", "Oracle Corporation", Match{"Oracle", "Java SE Runtime Environment", 1, "catalog:java-jre"}},
		{"win_registry", "Java(TM) SE Development Kit 17.0.12 (64-bit)", "Oracle Corporation", Match{"Oracle", "Java SE Development Kit", 1, "catalog:java-jdk"}},
		{"win_registry", "PowerShell 7.6.9.0-x64", "Microsoft Corporation", Match{"Microsoft", "PowerShell", 1, "catalog:powershell"}},
		{"win_registry", "PostgreSQL 13", "PostgreSQL Global Development Group", Match{"PostgreSQL Global Development Group", "PostgreSQL", 1, "catalog:postgresql"}},
		{"dpkg", "libssl3t64", "", Match{"OpenSSL", "OpenSSL", 1, "catalog:openssl"}},
		{"rpm", "openssl-libs", "", Match{"OpenSSL", "OpenSSL", 1, "catalog:openssl"}},
		{"dpkg", "linux-image-6.8.0-45-generic", "", Match{"Linux", "Linux kernel", 1, "catalog:linux-kernel"}},

		// Catálogo exige o fabricante quando o nome é fácil de imitar.
		{"win_registry", "Google Chrome", "Qualquer Um", Match{"Qualquer Um", "Google Chrome", 0.7, "heuristic:windows"}},

		// Pacote Linux fora do catálogo: o nome do pacote é o produto.
		{"dpkg", "curl", "Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>", Match{"", "curl", 1, "linux:package"}},

		// Heurística do Windows: só sugestão.
		{"win_registry", "Cursor (User)", "Anysphere", Match{"Anysphere", "Cursor", 0.7, "heuristic:windows"}},
		{"win_registry", "DBeaver 26.2.1 (current user)", "DBeaver Corp", Match{"DBeaver", "DBeaver", 1, "catalog:dbeaver"}},
		{"win_registry", "Cisco Packet Tracer 8.2.2 64Bit", "Cisco Systems, Inc.", Match{"Cisco Systems", "Packet Tracer", 0.7, "heuristic:windows"}},
		{"win_registry", "Ollama version 0.30.10", "Ollama", Match{"Ollama", "Ollama", 0.7, "heuristic:windows"}},
		{"win_registry", "Postman Agent x64 0.5.2", "Postman", Match{"Postman", "Agent", 0.7, "heuristic:windows"}},
		{"win_registry", "Bitvise SSH Client 9.66 (remove only)", "Bitvise Limited", Match{"Bitvise", "SSH Client", 0.7, "heuristic:windows"}},
		{"win_registry", "Advanced IP Scanner 2.5.1", "Famatech", Match{"Famatech", "Advanced IP Scanner", 0.7, "heuristic:windows"}},
		{"win_registry", "CPUID CPU-Z 2.17", "CPUID, Inc.", Match{"CPUID", "CPU-Z", 0.7, "heuristic:windows"}},
		{"win_registry", "Rustup: the Rust toolchain installer", "", Match{"", "Rustup: the Rust toolchain installer", 0.5, "heuristic:windows"}},
		{"win_registry", "NVIDIA Driver de gráficos 456.71", "NVIDIA Corporation", Match{"NVIDIA", "Driver de gráficos", 0.7, "heuristic:windows"}},
		{"win_registry", "12345", "", Match{}},
	} {
		got, ok := r.Evaluate(c.source, c.name, c.vendor)
		if !ok {
			got = Match{}
		}
		if got != c.want {
			t.Errorf("%s %q (%q):\n got %+v\nwant %+v", c.source, c.name, c.vendor, got, c.want)
		}
	}
}

func TestNormalizeThreshold(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Normalize("win_registry", "Cursor (User)", "Anysphere"); ok {
		t.Error("heurística não devia passar do limiar padrão")
	}
	if _, ok := r.Normalize("win_registry", "Google Chrome", "Google LLC"); !ok {
		t.Error("catálogo devia passar do limiar")
	}
	r.MinConfidence = 0.7
	if _, ok := r.Normalize("win_registry", "Cursor (User)", "Anysphere"); !ok {
		t.Error("com limiar 0,7 a heurística devia passar")
	}
}

func TestCatalogErrors(t *testing.T) {
	for _, bad := range []string{
		`[{"id":"a","product":"A","match":[{"sources":["dpkg"],"name":"("}]}]`,
		`[{"id":"a","product":"A","match":[{"sources":["dpkg"],"name":"a"}]},{"id":"a","product":"B","match":[{"sources":["dpkg"],"name":"b"}]}]`,
		`[{"id":"a","product":"A","match":[{"name":"a"}]}]`,
		`[{"id":"a","product":"","match":[{"sources":["dpkg"],"name":"a"}]}]`,
	} {
		if _, err := New([]byte(bad)); err == nil {
			t.Errorf("catálogo inválido aceito: %s", bad)
		}
	}
}
