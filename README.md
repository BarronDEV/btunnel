# 🌀 BTunnel

**Zero-Config, E2E Encrypted Peer-to-Peer Port Forwarding & Web Proxying over CGNAT.**

---

🌐 **Language Select / Dil Seçimi:**
- [English Documentation](#-english-documentation)
- [Türkçe Dokümantasyon](#-türkçe-dokümantasyon)

---

# 🇺🇸 English Documentation

BTunnel is a lightweight, high-performance networking utility that connects your local Docker networks or host ports directly to remote clients using end-to-end encrypted (E2EE) Peer-to-Peer (P2P) connections. It completely bypasses CGNAT and complex firewall setups without requiring traditional port forwarding or routing traffic through expensive cloud server relays.

## ✨ Features

- 🌀 **Zero Configuration**: No router setup, dynamic DNS, or port forwarding rules.
- 🔒 **End-to-End Encrypted (E2EE)**: Direct P2P tunnels secured via WebRTC DTLS 1.3. Even fallback TURN servers cannot inspect your traffic.
- 👥 **Multi-Client (Concurrent Connection)**: Multiple friends or clients can connect to your tunnels at the same time.
- 🐳 **Docker-Native Isolation**: Share entire Docker networks securely by launching isolated sidecars instead of exposing your host machine.
- 🌐 **Zero-Install Web Mode**: Share web applications directly with browser clients using a state-of-the-art Service Worker proxy.
- 🎮 **Mesh Mode (CLI-to-CLI)**: Direct TCP/UDP proxying for games (like Minecraft), SSH, databases, and microservices.
- 📊 **Beautiful Live TUI**: Real-time connection logging, bandwidth tracking, RTT display, and interactive dashboards powered by `bubbletea` & `lipgloss`.

---

## 🗺️ Architectural Overview

```
+------------------------------+
|  Host CLI (Go)               |
|  (Exposes local resource)    | <--- Signaling --->  +------------------------------+
+------------------------------+     (WebSockets)     |   Public Signaling Server    |
         ^                                            | (handshake.btunnel.dpdns.org)|
         |                                            +------------------------------+
         +=========== Direct E2EE P2P ===========+                    ^
                     (WebRTC DataChannel)                             |
         +=======================================+                    |
         v                                       v                    v
+------------------------------+       +---------------------------------------------+
|  Client CLI (Go)             |       |   Web Client (Service Worker SW)            |
|  (Bridges to local port)     |       |   (Directly loaded via public domain WSS)   |
+------------------------------+       +---------------------------------------------+
```

---

## 🛠️ Installation & Build

Ensure you have **Go 1.26+** installed.

### Option A: Install System-Wide (Recommended)
Install BTunnel globally so you can run it from anywhere:
```bash
go install github.com/barronDEV/btunnel@latest
```

### Option B: Run Directly (Without Compiling)
You can run any command directly from the Go source files:
```bash
# Share tunnels based on btunnel.json config
go run main.go run

# Join a mesh tunnel (Client)
go run main.go join bt-abc123xyz -l 25565
```

### Option C: Compile & Run Binary
Compile the Go source code into a native executable:
```bash
# Compile CLI binary
go build -o btunnel main.go
```

To run unit tests:
```bash
go test ./... -v -short
```

---

## 💻 Quick Start & Configuration Examples

To learn how to define tunnels in `btunnel.json` (Web and Mesh configurations, multi-tunnels, local embedded mode etc.), please review the usage guide:

[![Read Configuration & Examples Guide](https://img.shields.io/badge/Usage_Guide-Read_Examples-blue?style=for-the-badge&logo=markdown)](docs/configuration.md)

### 1. Share Tunnels (Host Side)
Ensure you have a `btunnel.json` in your directory, then run:
```bash
btunnel run
```

### 2. Join a Tunnel (Client Side)
Connect to a shared mesh tunnel by pasting the token (e.g. mapping to local port 25565):
```bash
btunnel join bt-a7f3c9e2b1d4f6a8 -l 25565
```

---

## ⚖️ How it Compares

| Feature | **BTunnel** | Ngrok / Cloudflare Tunnel | Tailscale / Wireguard |
|---|---|---|---|
| **Data Pathway** | **Direct P2P (No server relay)** | Cloud server relay | Mesh VPN (Whole machine exposed) |
| **Security** | **Target-only isolation** | Host exposed to proxy IP | Entire device put on network |
| **No-Install Mode**| **Yes (Service Worker Web)** | No | No |
| **Cost** | **Natively free & open-source** | Subscription tiers | Free tiers / Account required |

---

# 🇹🇷 Türkçe Dokümantasyon

BTunnel; yerel Docker ağlarınızı veya portlarınızı, uçtan uca şifreli (E2EE) Peer-to-Peer (P2P - Eşten Eşe) bağlantılar kullanarak uzaktaki istemcilere doğrudan bağlayan hafif ve yüksek performanslı bir ağ aracıdır. Geleneksel port yönlendirmeye gerek kalmadan veya trafiği bulut sunucularından geçirmeden CGNAT ve karmaşık güvenlik duvarlarını tamamen devre dışı bırakır.

## ✨ Özellikler

- 🌀 **Sıfır Yapılandırma**: Modem ayarı, dinamik DNS veya port yönlendirme kuralları gerektirmez.
- 🔒 **Uçtan Uca Şifreli (E2EE)**: WebRTC DTLS 1.3 ile korunan doğrudan P2P tüneller.
- 👥 **Eşzamanlı Çoklu Bağlantı**: Aynı anda birden fazla arkadaşınız veya istemci tünelinize paralel bağlanabilir.
- 🐳 **Docker Uyumlu Yalıtım**: Ana makinenizi internete maruz bırakmak yerine yalıtılmış bir sidecar konteyneri başlatarak tüm Docker ağlarını güvenle paylaşın.
- 🌐 **Kurulumsuz Web Modu**: Gelişmiş Service Worker proxy altyapısını kullanarak web uygulamalarını doğrudan tarayıcı istemcileriyle paylaşın.
- 🎮 **Mesh Modu (CLI-to-CLI)**: Oyunlar (Minecraft vb.), SSH, veritabanları ve mikroservisler için doğrudan TCP/UDP tünelleme.
- 📊 **Kullanışlı Canlı TUI**: `bubbletea` ve `lipgloss` ile güçlendirilmiş, gerçek zamanlı bağlantı günlüğü, bant genişliği takibi, RTT göstergesi ve etkileşimli dashboard'lar.
- 🚀 **Tek Komut**: `run` komutu ve `btunnel.json` ile tünellerinizi tek seferde ayağa kaldırın.

---

## 🛠️ Kurulum ve Derleme

**Go 1.26+** yüklü olduğundan emin olun.

### Seçenek A: Sisteme Global Kurulum (Önerilen)
BTunnel'i sisteme kurun ve her yerden kullanın:
```bash
go install github.com/barronDEV/btunnel@latest
```

### Seçenek B: Doğrudan Çalıştırın (Derlemeden)
```bash
# Tünelleri btunnel.json konfigürasyonuna göre paylaşın (Host)
go run main.go run

# Tünele bağlan (Client) — token'i yapıştırın
go run main.go join bt-abc123xyz -l 25565
```

### Seçenek C: Derleyin ve Binary Olarak Çalıştırın
```bash
# CLI uygulamasını derleyin
go build -o btunnel main.go
```

---

## 💻 Kullanım Kılavuzu & Örnekler

`btunnel.json` dosyasını hazırlama, tekli/çoklu tünel kurulumları ve lokal çevrimdışı kullanım detayları için aşağıdaki kılavuza göz atın:

[![Örnek Kullanım ve Yapılandırma Rehberi](https://img.shields.io/badge/Kullan%C4%B1m_K%C4%B1lavuzu-%C3%96rnekleri_G%C3%B6r-blue?style=for-the-badge&logo=markdown)](docs/configuration.md)

### 1. Paylaşım Başlatma (Host Tarafı)
Dizininde `btunnel.json` dosyası bulunduğundan emin olun ve ardından çalıştırın:
```bash
btunnel run
```

### 2. Tünele Bağlanma (Client Tarafı)
Paylaşılan bir mesh tüneline token ile hızlıca bağlanın (örn: yerel 25565 portuna):
```bash
btunnel join bt-a7f3c9e2b1d4f6a8 -l 25565
```

---

## ⚖️ Karşılaştırma Tablosu

| Özellik | **BTunnel** | Ngrok / Cloudflare Tunnel | Tailscale / Wireguard |
|---|---|---|---|
| **Veri Yolu** | **Doğrudan P2P (Sunucu aktarımı yok)** | Bulut sunucusu aktarımı | Mesh VPN (Tüm makine ağa açılır) |
| **Güvenlik** | **Yalnızca hedeflenen port yalıtılır** | Host proxy IP'sine açılır | Tüm cihaz ağa dahil edilir |
| **Kurulumsuz Mod**| **Evet (Service Worker Web)** | Hayır | Hayır |
| **Maliyet** | **Tamamen ücretsiz ve açık kaynak kodlu** | Ücretli abonelik katmanları | Ücretsiz katman limitleri / Hesap zorunlu |

---

## 🛡️ Güvenlik Matrisi

- **Yeniden Oynatma Koruması (Replay Protection)**: Mesajlar dinamik nonce'lar ve zaman damgası toleransları ile doğrulanır.
- **SSRF Önleme**: Gömülü TURN sunucularının trafiği iç özel ağlara yönlendirmesi kesinlikle engellenmiştir.
- **Dışa Doğru (Outbound-Only) İletişim**: Host CLI, sinyalleşme sunucusuna dışa doğru bağlantı başlatır. Bu sayede modeminizden içeriye doğru hiçbir port açmanıza gerek kalmaz.
