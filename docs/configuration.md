# 🌀 BTunnel — Configuration & Usage Kılavuzu

This document explains the configuration structure, multi-tunnel setups, and the multi-client network architecture of BTunnel.

🌐 **Language / Dil:**
- [English Guide](#-english-guide)
- [Türkçe Kılavuz](#-türkçe-kılavuz)

---

# 🇺🇸 English Guide

BTunnel is designed around a single configuration file (`btunnel.json`). You can share one or more ports simultaneously using `btunnel run`. 

## 1. Sharing Tunnels (`btunnel.json`)

To share services, create a `btunnel.json` file in your working directory.

### A. Single Web App Share Example
Save this as `btunnel.json` to share your local development web app running on port 3000:
```json
{
  "tunnels": {
    "my-web-app": {
      "type": "web",
      "target": "localhost:3000"
    }
  }
}
```
Then run the sharing command:
```bash
btunnel run
```

### B. Multi-Tunnel & Multi-Client Setup
You can define multiple tunnels in a single file. Thanks to the **Multi-Client (Concurrent Connection)** design, multiple external clients can connect to your tunnels at the same time:
```json
{
  "signal": "wss://handshake.btunnel.dpdns.org:8443/ws",
  "tunnels": {
    "my-website": {
      "type": "web",
      "target": "localhost:3000"
    },
    "ssh-access": {
      "type": "mesh",
      "target": "localhost:22"
    },
    "minecraft-server": {
      "type": "mesh",
      "target": "localhost:25565"
    }
  }
}
```

### Parameters
- `signal` *(optional)*: The custom signaling server URL. Defaults to `wss://handshake.btunnel.dpdns.org:8443/ws` if omitted.
- `tunnels`: A map of tunnels to share.
  - `type`: `web` (accessed via a web browser using the provided URL) or `mesh` (accessed via CLI-to-CLI TCP forwarding).
  - `target`: The local service address (port or host:port).

---

## 2. Joining Tunnels (Client Side)

How clients connect depends on the tunnel type.

### A. Web Mode (Browser Access - No Install)
If the tunnel is configured as `"type": "web"`, BTunnel will print a link on the host's terminal:
`https://handshake.btunnel.dpdns.org:8443/share/#bt-<TOKEN>`

Anyone can open this link in their web browser (on any device) to access your web application **without installing any software**.

### B. Mesh Mode (CLI-to-CLI)
For raw TCP/UDP services like Minecraft or SSH, your friends need to run the BTunnel binary.
```bash
# Map the shared tunnel to local port 25565
btunnel join bt-<TOKEN> -l 25565
```
Then, they can open their game/client and connect to `localhost:25565`. Multiple friends can run this command simultaneously to join the same server!

> [!TIP]
> **Headless / CLI-Only Servers:** This mode is perfect for servers that do not have a desktop interface or a web browser. You can bridge ports to query databases, curl local web endpoints, or access remote SSH setups directly from the terminal.

---

## 3. Local/Offline Mode (`--local`)

If you want to use BTunnel in a completely local or offline environment (LAN) without internet access:
```bash
# Starts an embedded local signaling server and shares tunnels
btunnel run --local
```
This generates a token containing your local IP (e.g. `bt-token@192.168.1.50:9090`). Clients on the same network can connect to this token directly.

---

# 🇹🇷 Türkçe Kılavuz

BTunnel, tamamen tek bir yapılandırma dosyası (`btunnel.json`) üzerine kuruludur. Tekli veya çoklu tünel paylaşımlarınızı bu dosya üzerinden `btunnel run` komutuyla başlatırsınız.

## 1. Tünel Paylaşımı (`btunnel.json`)

Hizmetlerinizi paylaşmak için çalışma dizininizde bir `btunnel.json` dosyası oluşturun.

### A. Tekli Web Paylaşım Örneği
Port 3000 üzerinde çalışan yerel web sitenizi paylaşmak için `btunnel.json` hazırlayın:
```json
{
  "tunnels": {
    "web-uygulamam": {
      "type": "web",
      "target": "localhost:3000"
    }
  }
}
```
Ardından şu komutla tüneli başlatın:
```bash
btunnel run
```

### B. Çoklu Tünel ve Çoklu İstemci Kurulumu
Aynı dosyada birden fazla tünel tanımlayabilirsiniz. Geliştirdiğimiz **Çoklu İstemci (Eşzamanlı Çoklu Bağlantı)** mimarisi sayesinde, aynı anda birden fazla arkadaşınız/müşteriniz tünelinize bağlanabilir:
```json
{
  "signal": "wss://handshake.btunnel.dpdns.org:8443/ws",
  "tunnels": {
    "web-portfolyom": {
      "type": "web",
      "target": "localhost:3000"
    },
    "ssh-erisimi": {
      "type": "mesh",
      "target": "localhost:22"
    },
    "minecraft-sunucum": {
      "type": "mesh",
      "target": "localhost:25565"
    }
  }
}
```

### Parametre Açıklamaları
- `signal` *(isteğe bağlı)*: Sinyalleşme sunucusunun adresi. Boş bırakılırsa varsayılan public sunucu kullanılır.
- `tunnels`: Paylaşılacak tünellerin listesi.
  - `type`: Tünel türü. `web` (tarayıcıdan kurulumsuz erişim) veya `mesh` (CLI-to-CLI TCP tünelleme) olmalıdır.
  - `target`: Paylaşılacak hedef yerel adres ve port.

---

## 2. Tünellere Bağlanma (İstemci Tarafı)

Tünel türüne göre istemcilerin bağlanma yöntemleri farklılık gösterir.

### A. Web Modu (Tarayıcı Erişimi - Kurulumsuz)
Eğer tünel türünüz `"type": "web"` ise, sunucu size tarayıcıda açabileceğiniz bir link verir:
`https://handshake.btunnel.dpdns.org:8443/share/#bt-<TOKEN>`

Bu linke tıklayan herkes, **bilgisayarına hiçbir şey indirmeden** web uygulamanıza doğrudan kendi tarayıcısından erişebilir.

### B. Mesh Modu (CLI-to-CLI)
Minecraft sunucuları, SSH veya veritabanları gibi raw TCP servisler için arkadaşlarınızın sadece BTunnel binary dosyasını indirmesi ve şu komutu koşturması gerekir:
```bash
# Paylaşılan tüneli yerelde 25565 portuna köprüleyin
btunnel join bt-<TOKEN> -l 25565
```
Ardından kendi oyunlarında/istemcilerinde adres olarak `localhost:25565` yazarak bağlanırlar. Çoklu istemci desteği sayesinde tüm arkadaşlarınız bu komutla aynı anda sunucuya katılabilir!

> [!TIP]
> **Masaüstü Olmayan (Headless) Sunucular:** Bu mod, masaüstü veya tarayıcı arayüzü olmayan Linux sunucularda (CLI-only / headless sunucular) curl, veritabanları veya SSH erişimleri için doğrudan komut satırı üzerinden kullanılabilir. Sunucunuzda yerel port açıp tünele köprü kurmak için mükemmel bir yöntemdir.

---

## 3. Lokal / Çevrimdışı Mod (`--local`)

Eğer internet erişimi olmayan kapalı bir yerel ağda (LAN) tünelleme yapmak istiyorsanız:
```bash
# Kendi içinde yerel bir sinyalleşme sunucusu başlatır ve paylaşır
btunnel run --local
```
Bu komut, yerel IP adresinizi içeren bir token üretir (örn: `bt-token@192.168.1.50:9090`). Aynı ağdaki istemciler bu token ile doğrudan size bağlanabilir.
