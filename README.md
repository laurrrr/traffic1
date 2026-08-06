# lantest — test de throughput și latență în LAN

Măsoară cât livrează efectiv rețeaua ta locală între laptop și telefon, și —
mai important — cu cât crește latența când legătura e încărcată. Pe laptop
rulează o aplicație cu fereastră proprie. Pe telefon nu instalezi nimic:
deschizi o adresă în browser, HTTP simplu, fără certificate.

Traficul e real: serverul trimite octeți dintr-un buffer aleator preîncărcat și
numără ce ajunge la celălalt capăt. Nu sunt cifre simulate.

Metrica-vedetă nu e viteza, ci **bufferbloat**: diferența dintre latența în
repaus și latența sub sarcină. Un Wi-Fi care dă 900 Mbps dar urcă de la 3 ms la
400 ms sub încărcare e o rețea pe care apelurile video se blochează.

---

## Cuprins

1. [Cerințe](#cerințe)
2. [Instalare](#instalare)
3. [Primul test, pas cu pas](#primul-test-pas-cu-pas)
4. [Opțiuni de linie de comandă](#opțiuni-de-linie-de-comandă)
5. [Ce vezi în terminal](#ce-vezi-în-terminal)
6. [Cum se folosește interfața](#cum-se-folosește-interfața)
7. [Arhitectură](#arhitectură)
8. [Metodologia măsurătorii](#metodologia-măsurătorii)
9. [Setările rețelei laptopului](#setările-rețelei-laptopului)
10. [Structura proiectului, fișier cu fișier](#structura-proiectului-fișier-cu-fișier)
11. [Limitările reale ale metodei](#limitările-reale-ale-metodei)
12. [Securitate](#securitate)
13. [Probleme frecvente](#probleme-frecvente)
14. [Dezvoltare](#dezvoltare)

---

## Cerințe

### Obligatoriu

| | Versiune | De ce |
|---|---|---|
| **Go** | 1.25 sau mai nou | Cerut de Wails v2.13 și de `golang.org/x/net`. Cu `GOTOOLCHAIN=auto` (implicit), Go descarcă singur toolchain-ul potrivit, deci merge și de pe o instalare mai veche. |
| **O rețea privată** | RFC 1918 / link-local | Unealta refuză să pornească dacă nu găsește nicio adresă privată. Vezi [Securitate](#securitate). |

Binarul **server-only** (build implicit) nu are nevoie de nimic altceva: e Go
pur, fără CGO, fără biblioteci de sistem.

### Doar pentru fereastra desktop

Shell-ul desktop folosește **Wails v2**, care randează prin webview-ul
sistemului de operare. Asta înseamnă CGO și biblioteci native:

| Platformă | Ce trebuie instalat |
|---|---|
| **Linux** | `libgtk-3-dev` și `libwebkit2gtk-4.1-dev` (sau `-4.0-dev` pe distribuții mai vechi) |
| **macOS** | Xcode Command Line Tools — `xcode-select --install`. WKWebView vine cu sistemul. |
| **Windows** | WebView2 Runtime, preinstalat pe Windows 11 și pe Windows 10 actualizat |

Pe Ubuntu/Debian:

```bash
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
```

### Opțional, la rulare

Detecția setărilor Wi-Fi (SSID, bandă, canal) apelează unelte de sistem. Dacă
lipsesc, aplicația merge normal — doar că panoul „Conexiunea acestui calculator"
va avea mai puține câmpuri.

| Platformă | Unealtă | Pachet |
|---|---|---|
| Linux | `iw` | `iw` |
| Linux | `nmcli` (completează securitatea și semnalul în %) | `network-manager` |
| macOS | `system_profiler`, `networksetup` | incluse în sistem |
| Windows | `netsh` | inclus în sistem |

### Dependențe Go

Trei directe, toate cu licențe permisive:

| Modul | Rol |
|---|---|
| `github.com/gorilla/websocket` | Conexiunile WebSocket ale protocolului de test |
| `github.com/skip2/go-qrcode` | Codul QR — în terminal și ca PNG la `/qr.png` |
| `github.com/wailsapp/wails/v2` | Fereastra desktop. Compilată **doar** cu tag-ul `desktop`; build-ul implicit nu o atinge. |

Frontend-ul are **zero** dependențe: fără npm, fără build step, fără CDN. Tot
HTML/CSS/JS-ul e scris de mână și embed-uit în binar cu `go:embed`.

---

## Instalare

```bash
git clone https://github.com/laurrrr/traffic1.git
cd traffic1
```

### Varianta 1 — server-only (recomandată pentru prima încercare)

Fără CGO, fără GUI, merge oriunde:

```bash
go build -o lantest .
./lantest
```

### Varianta 2 — cu fereastră desktop

**Ambele tag-uri sunt obligatorii:** `desktop` selectează shell-ul din acest
repo, `production` e cerut de Wails. Fără `production`, Wails compilează un stub
care eșuează la rulare — de aceea build-ul e configurat să refuze din start, cu
mesajul corect în eroarea de compilare.

| Platformă | Comandă |
|---|---|
| **Linux** (WebKitGTK 4.1) | `go build -tags "desktop,production,webkit2_41" -o lantest-desktop .` |
| **Linux** (WebKitGTK 4.0) | `go build -tags "desktop,production" -o lantest-desktop .` |
| **macOS** | `go build -tags "desktop,production" -o lantest-desktop .` |
| **Windows** | `go build -tags "desktop,production" -ldflags "-H windowsgui" -o lantest-desktop.exe .` |

Pe Windows, `-H windowsgui` scapă de fereastra de consolă din spatele
aplicației. Pentru binare mai mici, adaugă `-ldflags "-w -s"`.

> **Cross-compile nu funcționează pentru varianta desktop.** Wails are nevoie de
> CGO și de webview-ul nativ, deci fiecare platformă se compilează pe ea însăși.
> Varianta server-only se cross-compilează fără probleme.

---

## Primul test, pas cu pas

1. **Conectează laptopul la rețea.** Ideal pe cablu, dacă vrei să măsori
   Wi-Fi-ul telefonului izolat — altfel rezultatul include ambele legături
   wireless.

2. **Pornește serverul.**

   ```bash
   ./lantest
   ```

   Dacă vezi `no private LAN addresses found`, ești pe VPN sau pe o interfață
   publică. Deconectează VPN-ul.

3. **Citește bannerul.** Îți spune pe ce rețea ești, cum e conectat laptopul,
   unde se salvează istoricul și ce adrese poate folosi telefonul.

4. **Deschide pe telefon.** Scanează codul QR din terminal, sau tastează adresa
   afișată (`http://192.168.x.x:8080`). Telefonul trebuie să fie pe **aceeași
   rețea**. Dacă pagina nu se încarcă, vezi
   [Probleme frecvente](#probleme-frecvente) — cel mai des e izolarea clienților
   pe Wi-Fi.

5. **Alege setările** pe ecranul telefonului: direcție, durată, streamuri
   paralele. Implicit: ambele direcții, 10 secunde, 4 streamuri.

6. **Apasă „Pornește testul".** Ține ecranul aprins — un telefon care se blochează
   oprește timerele browserului, iar rularea e marcată ca nesigură.

7. **Urmărește pe laptop.** Ecranul desktop oglindește același grafic live, iar
   terminalul afișează fiecare stream cu tuplul lui TCP.

8. **Citește rezultatul.** Verdictul într-o frază, apoi cifrele. Detaliile
   tehnice sunt sub „Avansat".

Prima rulare durează ~25 de secunde (1 s latență în repaus + 10 s download +
10 s upload, plus conectare).

---

## Opțiuni de linie de comandă

```
./lantest -port 9090              # alt port (implicit 8080)
./lantest -streams 8              # streamuri paralele implicite: 1, 4 sau 8
./lantest -history /cale/f.json   # alt fișier de istoric
```

Numărul de streamuri dat aici e doar **valoarea implicită** a interfeței;
selectorul din pagină îl suprascrie pentru fiecare rulare.

---

## Ce vezi în terminal

### La pornire

```
  lantest — test de throughput și latență în LAN
  ──────────────────────────────────────────────
  Rețea:    Acasa_5G (192.168.1.0/24)
  Legătură: Wi-Fi Acasa_5G (5 GHz, canal 44/80 MHz, 802.11ax, -47 dBm) · rată radio 867 Mbps  [wlp3s0]
  Istoric:  /home/tu/.config/lantest/history.json

  Deschide pe telefon:

    http://192.168.1.10:8080

  [cod QR]
```

### La începutul unei rulări

```
── 12:46:06 Test pornit ───────────────────────────────────
   Client:    Android / Chrome (192.168.1.23)
   Sesiune:   06a286be04ed21fa
   Mod:       automat, 10 s pe direcție · download și upload · 4 streamuri · cadre de 64 KiB
   Streamuri:
     #0  192.168.1.23:35244 → 192.168.1.10:8080
     #1  192.168.1.23:35258 → 192.168.1.10:8080
     #2  192.168.1.23:35274 → 192.168.1.10:8080
     #3  192.168.1.23:35278 → 192.168.1.10:8080
```

Tuplurile TCP sunt acolo ca să poți confirma din `ss -tn` sau Wireshark că sunt
chiar N conexiuni separate, și pe ce porturi sursă.

### La finalul fiecărei direcții

```
── 12:46:16 Download încheiat (contorul serverului) ───────
     #    Octeți       Cadre      Mbps        Cotă   Blocaje
     0    154 MiB      2 456      128        25.2%         0
     1    152 MiB      2 436      127        25.0%         0
     2    153 MiB      2 444      127        25.0%         1
     3    152 MiB      2 426      126        24.9%         0
     tot  610 MiB      9 762      507                      1
```

**Coloana „Cotă" e motivul pentru care există tabelul.** Patru streamuri la 25%
fiecare și unul la 90% cu trei la 3% dau exact același total, dar numai primul e
o legătură sănătoasă. Când repartiția e vizibil strâmbă, tabelul o spune în text
și numește streamul.

„Blocaje" numără scrierile care au durat peste 200 ms — semn că celălalt capăt a
încetat să golească socket-ul.

Cifrele din tabel sunt ale **serverului**. La download cifra raportată în
rezultat rămâne a clientului; vezi
[Metodologia măsurătorii](#metodologia-măsurătorii).

## Cum se folosește interfața

Aceeași pagină rulează pe telefon și în fereastra desktop. Layout-ul se alege
după lățimea ecranului — „client" (un buton mare) sub 900 px, „host" (dashboard)
peste — și se poate forța din butonul **Vedere** din colț.

### Setări

Trei controale, pe ecranul de start:

| Control | Opțiuni | Implicit |
|---|---|---|
| **Direcție** | Ambele · Download · Upload | Ambele |
| **Durată** | 10 s fix · Până la Stop | 10 s fix |
| **Streamuri paralele** | 1 · 4 · 8 | 4 |

Alegerile se rețin în `localStorage`, deci rămân de la o sesiune la alta.

### Cele două moduri

| | Automat | Manual |
|---|---------|--------|
| Durată | 10 s pe direcție | până apeși **Oprește** |
| Direcție | ambele, sau doar una | o singură direcție |
| Istoric | comparabil între rulări | comparat doar cu alte rulări manuale de aceeași direcție |

Modul automat e cel calibrat: durată fixă, deci două rulări se pot compara
direct. Modul manual e pentru când vrei să vezi ce face legătura pe termen lung
— saturezi Wi-Fi-ul cât ai nevoie, te plimbi prin casă, și oprești când ai văzut
destul. Graficul acoperă toată rularea, indiferent cât a durat.

Rularea manuală merge într-o singură direcție: „ambele" ar cere două opriri
separate, ceea ce e un control confuz.

O rulare compară doar cu rulări de aceeași formă — același mod, aceeași
direcție, aceeași rețea. Un download manual de 4 minute și o rulare automată de
10 s în ambele sensuri nu măsoară același lucru.

### Al doilea ecran

Fereastra desktop nu e doar un loc unde ții codul QR. Cât timp telefonul
testează, laptopul **oglindește** același grafic live, prin progresul retransmis
de server. Un al treilea dispozitiv care are pagina deschisă vede și el aceeași
rulare, cu mențiunea „Rulează pe alt dispozitiv".

Ecranul de start arată și starea telefonului: „Aștept telefonul…", apoi
„Conectat: Android / Chrome — apasă Pornește pe telefon" din momentul în care
telefonul deschide pagina, apoi „Testează: Android / Chrome" în timpul rulării.

Dacă două dispozitive apasă Start simultan, al doilea primește un refuz explicit
și trece în modul oglindă. Vezi [Securitate](#securitate) pentru de ce nu se pot
rula două teste odată.

### Graficul interactiv

Graficul nu e doar o imagine. Trage peste el ca să mărești un interval, folosește
rotița ca să mărești în jurul cursorului, dublu-clic (sau dublu-tap) ca să revii
la toată rularea. Cursorul afișează valorile exacte din punctul respectiv:
momentul, throughput-ul și latența.

Zoom-ul nu e doar vizual — **scările verticale și decimarea se recalculează din
fereastra vizibilă**. Într-o rulare de câteva minute, graficul complet e redus la
900 de puncte (păstrând vârful fiecărui interval); dacă mărești pe 5 secunde,
vezi eșantioanele individuale, marcate cu puncte. Asta e diferența dintre a
mări o imagine și a rezolva detaliu.

Pe telefon, tragerea orizontală aparține graficului, iar cea verticală derulează
pagina în continuare.

Zoom-ul schimbă **doar imaginea**. Cifrele din carduri și din verdict se
calculează întotdeauna pe toată rularea — de asta scrie asta lângă grafic când e
mărit. Exportul PNG păstrează intervalul afișat și notează în subsol care e.

### Istoric și comparație

Fiecare rulare se salvează în directorul de configurare al utilizatorului
(`~/.config/lantest/history.json` pe Linux, echivalentul pe macOS/Windows),
maxim 200 de rulări. Rularea nouă se compară automat cu ultima rulare validă de
pe **aceeași rețea** — identificată prin SSID când se poate citi, altfel prin
subnet. Rulările întrerupte nu sunt folosite ca referință.

### Export

- **JSON** — `schema_version: 1`, rularea completă, rularea anterioară pe aceeași
  rețea, plus o secțiune `method` cu warmup-ul eliminat, lățimea ferestrei și
  care capăt a măsurat fiecare direcție.
- **PNG** — o imagine de sine stătătoare cu verdictul, cifrele și graficul,
  randată pe fundal deschis indiferent de tema aplicației.

În fereastra desktop se deschide dialogul nativ de salvare; în browser se
descarcă normal.

## Arhitectură

Un singur binar Go. Frontend-ul e unul singur, embed-uit cu `go:embed`, și
rulează identic în ambele locuri pentru că **nu folosește bindings Wails pentru
nimic din ce se măsoară** — vorbește exclusiv WebSocket cu serverul local:

```
fereastra desktop ──► http.Handler ──► ws://127.0.0.1:8080/ws
telefon (browser) ──► http.Handler ──► ws://192.168.x.x:8080/ws
                       (același)          (același protocol)
```

Bindings-urile Wails există doar pentru ce un tab de browser nu poate face:
dialogul nativ de salvare și deschiderea unui link în browserul sistemului.
Ambele sunt feature-detected, deci lipsa lor nu schimbă nimic pe telefon.

### Modelul de conexiuni

O rulare folosește mai multe conexiuni WebSocket cu același ID de sesiune,
generat de client. Prima cerere pe fiecare conexiune trebuie să fie un `HELLO`
care declară rolul:

| Rol | Câte | Ce duce |
|---|---|---|
| **control** | exact una | PING/PONG pe toată durata rulării, progresul retransmis, rezultatul final. Ocuparea acestui rol ia lacătul de test unic. |
| **stream** | 1, 4 sau 8 | doar date în vrac |
| **observer** | oricâte | evenimente de stare și progres; niciodată blocat de lacăt |

Separarea nu e cosmetică: pe conexiunea de control **nu circulă date în vrac**,
și exact de asta un ping poate primi răspuns în timp ce legătura e saturată. Dacă
ping-urile ar merge pe aceeași conexiune cu datele, ar sta la coadă în spatele
lor și ar măsura coada, nu rețeaua.

### Ciclul unei rulări

```
client                                            server
  │                                                 │
  │  HELLO (control, sessionId, mod, direcție)      │
  ├────────────────────────────────────────────────►│  ia lacătul de test
  │◄────────────────────────────────────────────────┤  HELLO_ACK
  │                                                 │
  │  HELLO (stream #0..#N)                          │
  ├────────────────────────────────────────────────►│  leagă streamurile de sesiune
  │◄────────────────────────────────────────────────┤  HELLO_ACK ×N
  │                                                 │
  │  ── faza 1: latență în repaus ──                │
  │  PING ×50, la 20 ms                             │
  ├────────────────────────────────────────────────►│  ecou imediat
  │◄────────────────────────────────────────────────┤  PONG ×50
  │                                                 │
  │  ── faza 2: download ──                         │
  │  DOWN_START pe fiecare stream                   │
  ├────────────────────────────────────────────────►│  ┌ afișează antetul rulării
  │◄══════ DOWN_DATA ×mii ═══════════════════════════┤  │ scrie din bufferul aleator
  │  PING la 100 ms ──────────────────────────────► │  │ (pe conexiunea de control)
  │◄────────────────────────────────────────────────┤  │ PONG
  │◄────────────────────────────────────────────────┤  └ DOWN_DONE + tabel per stream
  │                                                 │
  │  ── faza 3: upload ──                           │
  │  UP_START pe fiecare stream                     │
  ├────────────────────────────────────────────────►│
  ├══════ UP_DATA ×mii ═════════════════════════════►│  numără în ferestre aliniate
  │  PING la 100 ms ──────────────────────────────► │
  │  UP_DONE ───────────────────────────────────────►│
  │◄────────────────────────────────────────────────┤  RESULT (agregat) + tabel
  │                                                 │
  │  FINAL (eșantioane brute, RTT-uri brute)        │
  ├────────────────────────────────────────────────►│  taie warmup, calculează
  │◄────────────────────────────────────────────────┤  STORED (rulare + precedenta)
  │                                                 │
  └── închide conexiunile ─────────────────────────►│  eliberează lacătul
```

În modul manual, faza de încărcare nu are durată: se termină când operatorul
apasă Stop. Download-ul se oprește pe server (bucla de trimitere verifică un flag
între cadre), upload-ul se oprește pe client.

### Protocolul pe fir

Cadre WebSocket binare. Octetul 0 e tipul mesajului, restul e încărcătura:

| Cod | Mesaj | Direcție | Încărcătură |
|---|---|---|---|
| `0x01` | `PING` | C→S | timestamp float64, 8 octeți |
| `0x02` | `PONG` | S→C | ecou exact al PING-ului |
| `0x03` | `DOWN_START` | C→S | JSON: durată, dimensiune cadru, manual |
| `0x04` | `DOWN_DATA` | S→C | octeți din bufferul preîncărcat |
| `0x05` | `DOWN_DONE` | S→C | JSON: contorul serverului pentru acest stream |
| `0x06` | `UP_START` | C→S | JSON: durată, manual |
| `0x07` | `UP_DATA` | C→S | octeți de test |
| `0x08` | `UP_DONE` | C→S | (gol) |
| `0x09` | `RESULT` | S→C | JSON: upload agregat pe streamuri |
| `0x0A` | `ABORT` | ambele | (gol) |
| `0x0B` | `HELLO` | C→S | JSON: sesiune, rol, index, mod, direcție |
| `0x0C` | `HELLO_ACK` | S→C | JSON: confirmare |
| `0x0D` | `BUSY` | S→C | JSON: motivul refuzului |
| `0x0E` | `PROGRESS` | C→S | JSON: metrici live, retransmise observatorilor |
| `0x0F` | `OBSERVE` | S→C | JSON: eveniment pentru al doilea ecran |
| `0x10` | `FINAL` | C→S | JSON: măsurători brute |
| `0x11` | `STORED` | S→C | JSON: rularea salvată + cea precedentă |
| `0x12` | `STOP` | C→S | (gol) — încheie o rulare manuală |

De ce binar și nu JSON peste tot: `DOWN_DATA` și `UP_DATA` sunt zeci de mii de
cadre pe rulare. Un prefix de un octet costă nimic; JSON ar costa CPU care s-ar
scădea din ce se măsoară.

### Endpoint-uri HTTP

Aceleași pe toate listener-ele — LAN, loopback, și fereastra desktop:

| Rută | Ce întoarce |
|---|---|
| `/` | frontend-ul embed-uit |
| `/ws` | protocolul de test (WebSocket) |
| `/config.js` | configurația injectată în pagină: port, streamuri implicite, warmup |
| `/api/urls` | adresele LAN pe care le poate folosi telefonul |
| `/api/link` | cum e conectat laptopul (cache, vezi [mai jos](#setările-rețelei-laptopului)) |
| `/api/history` | rulările salvate (doar citire) |
| `/qr.png` | codul QR ca imagine |

Codul QR e randat de **server**, nu de o bibliotecă JavaScript. Așa se respectă
promisiunea „zero CDN, fără build step" fără să bagi câteva mii de linii de
encoder în pagină.

### Cele două shell-uri

Aceeași bază de cod produce două binare diferite, alese prin build tags:

| Build | Tag | Ce face | Are nevoie de |
|---|---|---|---|
| implicit | — | pornește serverul, afișează bannerul, așteaptă Ctrl-C | nimic |
| desktop | `desktop,production` | deschide o fereastră Wails care servește **același** `http.Handler` | CGO + webview nativ |

Motivul pentru care implicitul e cel fără fereastră: `go build ./...`, `go vet`
și `go test` trebuie să meargă pe orice mașină și în CI, fără GTK instalat.

## Metodologia măsurătorii

| Metrică | Metodă |
|---------|--------|
| Latență în repaus | 50 dus-întors pe conexiunea de control; min / p50 / p95 / jitter |
| Download | Serverul trimite 10 s pe N streamuri; **clientul** numără octeții |
| Upload | Clientul trimite 10 s pe N streamuri; **serverul** numără octeții |
| Latență sub sarcină | Ping continuu (100 ms) în timpul download-ului și al upload-ului |
| Bufferbloat | p95 sub sarcină − p50 în repaus, cu notă de la A la F |
| Verificări de încredere | blocaj al firului principal, ping-uri fără răspuns, tampon implicat imposibil |
| Viteză min / max | extremele ferestrelor de 250 ms, după eliminarea warmup-ului |
| Cadre și dimensiune | mesaje WebSocket numărate de capătul care le primește |
| Pierdere de pachete | **NEMĂSURAT** — TCP ascunde retransmisiile |

Fiecare direcție e numărată de capătul care știe adevărul. La download,
`WriteMessage` care întoarce `nil` înseamnă doar că nucleul a acceptat octeții
în bufferul socket-ului, nu că au ajuns la client — deci cifra raportată e a
clientului. La upload e invers. Contorul serverului pentru download se păstrează
ca diagnostic: dacă diferă cu peste 5%, rularea primește un avertisment explicit.

Primele **1000 ms din fiecare direcție se aruncă** înainte de calculul
percentilelor. TCP slow-start plus adaptarea de rată Wi-Fi fac prima secundă
nereprezentativă; incluzând-o, p50 scade și rulările nu mai sunt comparabile.
Fiecare export notează că s-a întâmplat. Se aruncă și ultima fereastră de
eșantionare, pentru că streamurile nu se opresc perfect simultan și ar arăta ca
o prăbușire de throughput care nu a existat.

Se raportează percentile, niciodată medii goale: o singură fereastră blocată
strică o medie.

### „Cadre", nu pachete

Se raportează numărul de **mesaje WebSocket** și dimensiunea încărcăturii utile
a unuia (64 KiB implicit), nu pachete IP. TCP re-segmentează după MTU-ul căii —
un cadru de 64 KiB devine în jur de 45 de segmente pe o cale obișnuită, iar dacă
e activ TSO/GSO nici măcar nucleul nu vede aceeași împărțire ca firul. Numărul
real de pachete de pe fir nu poate fi observat dintr-un browser, așa că nu e
raportat: ar fi o cifră inventată.

Cadrele de download sunt numărate de client, cele de upload de server — același
principiu ca la octeți: numără capătul care știe ce a ajuns.

### Streamuri paralele

Un singur stream TCP rareori saturează Wi-Fi-ul modern. Throughput-ul se agregă
**însumând octeții din aceeași fereastră de timp** pe toate streamurile. Media ar
sub-raporta legătura de N ori; concatenarea ar număra aceeași secundă de mai
multe ori. Ferestrele sunt aliniate la un moment de start comun întregii
sesiuni, ceea ce face suma validă.

### Când o cifră e refuzată

Nota de bufferbloat e dată doar dacă poate fi atribuită rețelei. Verificările se
aplică **doar notelor care acuză** (creștere peste 30 ms, adică nota C sau mai
rea): la A sau B concluzia e „legătura e în regulă sub sarcină", și asta rămâne
adevărat chiar dacă o parte din milisecunde au venit de la browser. Trei condiții
invalidează o notă, fiecare măsurată separat:

1. **Firul principal al browserului a fost blocat** — un timer de 100 ms care
   întârzie mult înseamnă că pagina și-a măsurat propria întârziere.
2. **Ping-urile nu s-au întors** — dacă sub jumătate primesc răspuns, cele care
   ajung sunt prin construcție coada cea mai lentă.
3. **Tamponul implicat e imposibil** — `întârziere × debit` dă câți octeți ar fi
   trebuit să stea în coadă undeva pe drum. Peste 256 MB, coada nu e în rețea.

Când vreuna se declanșează, rularea e marcată nesigură, insigna arată `?` în loc
de o notă, iar verdictul spune ce s-a întâmplat de fapt.

### Statistica trăiește într-un singur loc

Frontend-ul nu calculează nimic. Trimite ferestrele brute și timpii de
dus-întors bruți; serverul face tăierea, percentilele și notarea. Așa nu există
o implementare în Go și una în JavaScript care să divergă, iar telefonul,
fereastra desktop și fișierul de istoric nu pot fi în dezacord.

## Setările rețelei laptopului

Ecranul de start arată cum e conectat calculatorul pe care rulează serverul:
cablu sau Wi-Fi, iar dacă e Wi-Fi — SSID, bandă, canal, lățime de canal,
standard 802.11, semnal, securitate, BSSID și rata radio negociată. Aceleași
date se salvează cu fiecare rulare și apar sub „Avansat", pentru că **același
SSID pe 2.4 GHz și pe 5 GHz sunt rețele diferite** din punct de vedere al
debitului, iar o rulare fără contextul ăsta nu se poate interpreta mai târziu.

Detecția e best effort și diferă per sistem:

| Sistem | Unealtă | Ce obține |
|--------|---------|-----------|
| **Linux** | `iw dev <if> link`, completat cu `nmcli` | SSID, BSSID, frecvență, semnal în dBm, rate, lățime; securitate din nmcli |
| **macOS** | `system_profiler SPAirPortDataType` | SSID, standard PHY, canal + bandă + lățime, securitate, semnal/zgomot, rată |
| **Windows** | `netsh wlan show interfaces` | SSID, BSSID, tip radio, bandă, canal, rate, semnal în procente |
| Cablu, Linux | `/sys/class/net/<if>/speed` și `/duplex` | viteza portului și duplex |
| Cablu, macOS | `networksetup -getmedia` | la fel |

Ce nu se poate citi lipsește pur și simplu; nimic de aici nu poate opri
aplicația. Banda și canalul se derivă din frecvență când unealta dă doar
frecvența — cele trei benzi își numerotează canalele de la ancore diferite, așa
că nu e o singură formulă.

Două lucruri de reținut:

- **Rata radio nu e o măsurătoare.** E viteza negociată a legăturii, adică
  plafonul teoretic. Testul măsoară ce livrează efectiv rețeaua, și e normal să
  fie considerabil mai mică.
- **Sondarea nu atinge niciodată un test.** Se face la pornire și apoi în fundal
  la 30 s, iar rezultatul e păstrat în cache; `system_profiler` poate dura
  secunde, și nici încărcarea paginii nici finalul unui test nu au voie să
  aștepte după el.

## Structura proiectului, fișier cu fișier

```
traffic1/
├── protocol.go            definițiile de pe fir: tipuri de mesaje, structuri
├── stats.go               toată matematica: percentile, agregare, bufferbloat
├── network.go             adrese private, identitatea rețelei
├── link.go                cum e conectat laptopul (Wi-Fi/cablu, bandă, canal)
├── history.go             persistența rulărilor
├── server.go              serverul HTTP + WebSocket, sesiuni, fazele de test
├── streamlog.go           tabelele per stream din terminal
├── main.go                pornire: flag-uri, banner, listener-e
├── shell_headless.go      build implicit: fără fereastră
├── shell_desktop.go       build `desktop`: fereastra Wails
├── shell_desktop_guard.go oprește build-ul dacă lipsește tag-ul `production`
├── web/
│   ├── index.html         structura paginii
│   ├── style.css          două layout-uri, temă clară/întunecată
│   └── app.js             clientul: protocol, orchestrare, grafic, export
├── *_test.go              104 teste
└── .github/workflows/ci.yml
```

**Regula care ține totul laolaltă:** frontend-ul nu calculează nicio statistică.
Trimite eșantioane brute și timpi de dus-întors bruți; serverul face tăierea,
percentilele și notarea. Așa nu există o implementare în Go și una în JavaScript
care să divergă.

---

### `protocol.go` — 264 rânduri

Definește „limba" dintre client și server. Nu conține logică, doar contractul.

- **18 constante de tip de mesaj** (`MsgPing` … `MsgStop`), fiecare cu un
  comentariu care spune direcția, rolul de conexiune și forma încărcăturii.
- **Constantele de rol** (`control`, `stream`, `observer`), de mod (`auto`,
  `manual`) și de direcție (`both`, `download`, `upload`).
- **Structurile JSON** ale fiecărui mesaj: `Hello`, `HelloAck`, `Busy`,
  `DownStartCfg`, `DownDoneMsg`, `UpStartCfg`, `UpResultMsg`, `Progress`,
  `FinalMsg`, `StoredMsg`, `ObserveEvent`, `PeerInfo`, `StallEvent`.

Comentariul de deschidere documentează și **seam-ul pentru WebRTC DataChannel** —
interfața `Transport` care ar trebui implementată, unde se schimbă tipul concret,
și de ce doar rolul „stream" ar avea nevoie de transport nesigur. Nu e
implementat; e scris ca să nu trebuiască redescoperit.

`FinalMsg` merită o privire: e **deliberat brut**. Clientul trimite ferestre de
eșantionare netăiate și RTT-uri netăiate, nu cifre gata calculate. Cifrele de
upload nu apar deloc — serverul le-a măsurat el însuși.

### `stats.go` — 484 rânduri

Toată matematica, în funcții pure. Singurul loc unde se calculează ceva.

| Funcție | Ce face |
|---|---|
| `Mbps` | octeți peste durată → megabiți pe secundă |
| `Percentile` | percentilă interpolată liniar; nu mutează intrarea |
| `AggregateSamples` | **însumează** ferestrele aliniate ale streamurilor paralele |
| `TrimWarmup` | aruncă primele 1000 ms (TCP slow-start) |
| `TrimTail` | aruncă ultima fereastră (streamurile nu se opresc simultan) |
| `Summarize` | serie de eșantioane → min/p50/p95/max/medie |
| `Jitter` | diferența medie absolută între RTT-uri consecutive (stil RFC 3550) |
| `SummarizeLatency` | RTT-uri → statistici + `Sent` pentru rata de răspuns |
| `GradeBufferbloat` | milisecunde în plus → notă A–F |
| `ComputeBufferbloat` | nota, plus **cele trei verificări de încredere** |
| `BuildVerdict` | cifrele → o frază în română, conștientă de direcție |
| `clampDuration` | mărginește durata cerută de client |

Tipurile: `Sample`, `DirStats`, `LatencyStats`, `LatencyReport`, `Bufferbloat`,
`FrameStats`.

Două lucruri de reținut din acest fișier. **`AggregateSamples` însumează, nu
mediază** — patru streamuri se adună în aceeași fereastră de timp; media ar
sub-raporta legătura de patru ori. Și **`ComputeBufferbloat` poate refuza să dea
o notă**, dacă firul principal al browserului a fost blocat, dacă ping-urile nu
s-au întors, sau dacă întârzierea măsurată ar cere mai mult tampon decât poate
exista fizic în rețea.

### `network.go` — 217 rânduri

Ce adrese are mașina și cum se numește rețeaua.

- `getLANAddresses` — enumeră interfețele și păstrează **doar** adresele private.
  IPv6 link-local e sărit intenționat: are nevoie de zone ID (`fe80::1%en0`), pe
  care browserele nu îl acceptă în URL.
- `isPrivateV4` / `isPrivateV6` / `isPrivateHost` — politica de adrese. Numele de
  host sunt respinse: un nume poate rezolva oriunde.
- `subnetOf`, `formatURL`, `formatListenAddr`, `hostOnly` — formatare, cu grijă
  la parantezele IPv6.
- `identifyNetwork` — cheia sub care se grupează rulările: SSID dacă se poate
  citi, altfel subnet. **Funcție pură** — SSID-ul vine ca argument.
- `describeUA` — User-Agent → „Android / Chrome". Deliberat grosier: e o etichetă
  în interfață, nu o intrare într-o decizie.

### `link.go` — 635 rânduri

Cum e conectat laptopul. Cel mai mare fișier după `server.go`, aproape tot
parsare.

**Partea pură** (testabilă fără sistemul de operare):
`bandFromFreq`, `channelFromFreq` — cele trei benzi își numerotează canalele de
la ancore diferite, deci nu e o singură formulă.

**Parserele**, câte unul per unealtă, fiecare o funcție de la text la `LinkInfo`:

| Funcție | Unealtă | Capcana pe care o rezolvă |
|---|---|---|
| `parseIWLink` | `iw dev X link` | lățimea de canal e ascunsă în linia de bitrate |
| `parseNmcliWifi` | `nmcli dev wifi` | nmcli escapează două puncte în BSSID; un `split(":")` naiv sparge rândul |
| `parseSystemProfilerAirPort` | `system_profiler` | listează rețelele vecine *după* cea conectată — parsarea trebuie să se oprească |
| `parseNetshWlan` | `netsh wlan show interfaces` | etichetele sunt **localizate** |
| `parseNetworksetupMedia` | `networksetup -getmedia` | cablu: viteză și duplex |

**Partea care atinge sistemul**: `detectLinkLinux` / `Darwin` / `Windows`,
`isWirelessLinux` (întreabă sysfs, nu ghicește după numele interfeței),
`runTool` (fiecare comandă are timeout de 6 s), și `safeIface` — o expresie
regulată care validează numele interfeței înainte să ajungă pe o linie de
comandă.

**`LinkMonitor`** ține un snapshot în cache, reîmprospătat în fundal la 30 s.
Există pentru că `system_profiler` poate dura secunde, iar nici încărcarea
paginii nici finalul unui test nu au voie să aștepte după el.

### `history.go` — 199 rânduri

Persistența. Structura `Run` e forma completă a unui rezultat salvat: identitate,
rețea, mod, direcție, legătură, cadre, ambele direcții, latență, bufferbloat,
verdict, caveats.

`HistoryStore` scrie **atomic**, printr-un fișier temporar plus `rename`, ca o
cădere la mijlocul scrierii să nu lase un istoric trunchiat. Citirea filtrează
rulările cu altă versiune de schemă în loc să ghicească, și un fișier corupt e
raportat și înlocuit — nu oprește aplicația.

`Append` alege și rularea precedentă **comparabilă**: aceeași rețea, același mod,
aceeași direcție, neîntreruptă. Un download manual de 4 minute și o rulare
automată de 10 s în ambele sensuri nu măsoară același lucru.

### `server.go` — 1146 rânduri

Inima. Merită citit pe bucăți.

**`wsConn`** — un wrapper peste conexiunea gorilla care serializează scrierile.
Necesar pentru că rezultatele se scriu dintr-o altă goroutină decât cea care
citește, iar gorilla permite exact un scriitor.

**`Session`** — o rulare logică: conexiunea de control plus streamurile ei.
Ține flag-ul de stop pentru modul manual, eșantioanele de upload per stream,
și rezultatele per stream pentru tabelele din terminal.

**`Server`** — lacătul de test unic, mulțimea de observatori, istoricul,
monitorul de legătură.

**`checkOrigin`** — refuză orice nu e adresă privată sau loopback. Verifică
`Host`-ul (politica de legare) **și** `Origin`-ul (asta e ce oprește efectiv o
pagină de pe internet să comande serverul prin browserul tău).

**Handlerele HTTP**: `handleWS`, `handleConfig`, `handleHistory`, `handleURLs`,
`handleLink`, `handleQR`.

**Cele trei roluri**: `serveControl` (ia lacătul, duce PING/PONG, primește
FINAL), `serveStream` (execută fazele), `serveObserver` (primește difuzările,
niciodată blocat).

**Fazele**: `sendDownload` scrie într-o buclă strânsă din bufferul preîncărcat,
cu deadline pe fiecare scriere și detecție de blocaj; `recvUpload` citește și
grupează octeții în ferestre aliniate la un moment de start comun sesiunii.

**`handleFinal`** — locul unde se compune rezultatul: taie warmup-ul, cheamă
statisticile, compară contorul clientului cu al serverului, calculează
bufferbloat-ul și verdictul, salvează, difuzează.

### `streamlog.go` — 215 rânduri

Tabelele din terminal, ca funcții pure peste structuri simple.
`formatRunHeader` afișează tuplurile TCP la începutul rulării;
`formatStreamTable` afișează repartiția la final; `balanceNote` semnalează un
stream înfometat, cu praguri puse mult în afara variației normale TCP ca o
rulare obișnuită să rămână tăcută.

### `main.go` — 156 rânduri

Pornirea, în ordine: parsează flag-urile, enumeră adresele private (și **refuză
să pornească** dacă nu găsește niciuna), sondează legătura o dată, identifică
rețeaua, construiește serverul, afișează bannerul cu QR, deschide **câte un
listener per adresă** plus loopback, apoi predă controlul shell-ului.

Legarea per adresă în loc de `0.0.0.0` e intenționată: unealta nu trebuie să fie
accesibilă de pe o interfață publică nici din greșeală.

### Cele trei shell-uri

| Fișier | Tag | Rol |
|---|---|---|
| `shell_headless.go` (24) | `!desktop` | așteaptă Ctrl-C. Build-ul implicit. |
| `shell_desktop.go` (88) | `desktop` | deschide fereastra Wails servind **același** `http.Handler`. Bindings doar pentru dialogul de salvare și deschiderea unui link. |
| `shell_desktop_guard.go` (18) | `desktop && !production` | **oprește compilarea** cu numele fixului în mesaj |

Fișierul-gardă există dintr-un motiv concret: fără tag-ul `production`, Wails
compilează un stub care eșuează *la rulare*, după ce serverul a pornit deja și a
afișat codul QR. Arăta ca un bug de server. Acum e o eroare de compilare care
spune ce să scrii.

---

### Frontend

#### `web/index.html` — 250 rânduri

Structura paginii. Panourile pentru fiecare stare (așteptare / rulare /
rezultat), cardurile de rezultat, tabelul „Avansat", panoul cu legătura
laptopului. Vizibilitatea nu e controlată din JavaScript, ci prin atributele
`data-state` și `data-layout` de pe elementul rădăcină.

#### `web/style.css` — 486 rânduri

Un singur stylesheet, două layout-uri. Variabile CSS pentru temă, cu
`prefers-color-scheme` pentru modul întunecat. Layout-ul „host" și „client"
sunt selectate prin `data-layout`, iar panourile prin `data-state` — deci
schimbarea stării e o singură atribuire, nu o listă de `style.display`.

#### `web/app.js` — 2159 rânduri

Clientul. Fără framework, fără build step, fără dependențe.

- **Transport** — `openConn` face handshake-ul HELLO și respinge cu `BusyError`
  când lacătul e luat; `sendRaw` / `sendJSON` construiesc cadre binare.
- **`Chart`** — canvas cu două axe, benzi de fază, zoom prin tragere, rotiță,
  cruce cu citire exactă. Scările verticale și decimarea se recalculează din
  fereastra vizibilă, deci mărirea chiar rezolvă detaliu.
- **Orchestrarea rulării** — `runIdleLatency`, `runDownload`, `runUpload`,
  `sendFinal`, plus `stopTest` pentru modul manual.
- **Contrapresiune** — `pump` umple socketul dar se oprește la 4 MiB în coadă;
  `bufferedAmount` e singurul semnal de contrapresiune pe care îl dă un
  WebSocket, iar ignorarea lui ar raporta un debit pe care rețeaua nu l-a livrat.
- **Măsurarea propriei sănătăți** — un timer separat măsoară cât de târziu se
  declanșează, ca serverul să poată decide dacă latența descrie rețeaua sau
  browserul.
- **Randarea** — rezultate, comparație, istoric, panoul de legătură, export JSON
  și PNG.
- **Modul observator** — aceeași pagină oglindește rularea altui dispozitiv.

---

### Teste — 104 în total

| Fișier | Teste | Ce acoperă |
|---|---|---|
| `stats_test.go` | 34 | percentile, agregarea pe streamuri, tăierea warmup-ului, bufferbloat și cele trei verificări de încredere, verdictul |
| `link_test.go` | 20 | parserele pentru toate cele trei sisteme, cu ieșire capturată reală |
| `server_test.go` | 17 | politica de Origin, handshake, refuzul concurenței, **ciclu complet** împotriva unui server real, modul manual |
| `network_test.go` | 13 | detecția adreselor private, subnet, User-Agent |
| `streamlog_test.go` | 11 | formatarea tabelelor, detecția streamului înfometat |
| `history_test.go` | 9 | persistență, comparație, fișier corupt, plafon |

Parserele din `link_test.go` sunt singurul mod în care căile macOS și Windows pot
fi verificate din altă parte: fiecare parser e o funcție pură peste text, testată
cu ieșire realistă capturată de pe sistemele respective.

`server_test.go` pornește un server adevărat cu `httptest`, conectează clienți
WebSocket și mută octeți reali prin protocol — inclusiv un ciclu complet de la
HELLO până la rezultatul salvat.

### Meta

| Fișier | Rol |
|---|---|
| `go.mod` / `go.sum` | trei dependențe directe, restul tranzitive prin Wails |
| `.github/workflows/ci.yml` | gofmt + vet + teste cu `-race`, build server-only pe Linux/macOS/Windows, build desktop nativ pe fiecare |
| `LICENSE` | MIT |
| `.gitignore` | binarele produse de build |

## Limitările reale ale metodei

Astea nu sunt detalii de subsol, sunt motivele pentru care unele cifre nu
înseamnă ce par să spună:

- **Pierderea de pachete nu se poate măsura peste TCP.** TCP retransmite tăcut;
  ce vezi e throughput redus și latență crescută, nu pachete lipsă. Orice unealtă
  care raportează „0% packet loss" peste TCP raportează o tautologie. Aici scrie
  explicit NEMĂSURAT.
- **Se măsoară calea end-to-end, nu doar Wi-Fi-ul.** Dacă laptopul e pe Wi-Fi,
  rezultatul include ambele legături wireless plus router-ul. Pentru a izola
  Wi-Fi-ul telefonului, pune laptopul pe cablu.
- **Browserul e un instrument de măsură imperfect.** Ecranul stins sau tab-ul în
  fundal opresc timerele și blochează socket-ul; rularea e marcată ca nesigură,
  nu raportată ca normală.
- **Pe legături foarte rapide, dispozitivul devine el însuși bufferul.** Un
  browser livrează toate mesajele WebSocket pe un singur fir. Peste câțiva Gbps
  (bucla locală, 2,5/10 GbE) răspunsul la un ping ajunge să stea în coadă în
  spatele datelor de test, iar round-trip-ul măsurat include acea coadă. Firul
  principal poate arăta perfect sănătos în tot acest timp — timerele se declanșează
  la vreme, toate ping-urile primesc răspuns — deci nicio verificare din client nu
  vede problema. Ce o prinde e fizica: întârzierea de așteptare înseamnă tampon
  împărțit la rată, așa că unealta calculează cât tampon ar fi implicat de
  creșterea măsurată. Peste 256 MB refuză să dea o notă și spune de ce, în loc să
  raporteze „bufferbloat foarte sever" pentru ceva ce e de fapt coada propriului
  browser. La viteze de Wi-Fi asta nu se întâmplă niciodată.
- **Throughput-ul de upload live e o estimare.** Clientul poate ști doar câți
  octeți a predat lui `send()` minus ce e încă în coadă. Cifra finală vine de la
  server.
- **Bufferbloat-ul măsurat e al căii de test.** Un ping ICMP către gateway ar
  măsura altceva; aici se măsoară latența traficului care chiar concurează cu
  testul.
- **Un singur test odată.** Al doilea client primește un refuz explicit, nu cifre
  false. Două rulări simultane ar împărți banda și ambele ar greși.
- **Rulările nu se compară între rețele.** De asta istoricul e cheiat pe SSID sau
  subnet.

## Securitate

Serverul se leagă **doar** la adrese private: RFC 1918, link-local, ULA IPv6 și
loopback — câte un listener per adresă, niciodată `0.0.0.0`. Refuză să pornească
dacă nu găsește nicio adresă privată.

WebSocket-ul verifică atât `Host`-ul (trebuie să fie IP privat sau loopback;
numele de host sunt respinse, pentru că un nume poate rezolva oriunde) cât și
`Origin`-ul, când există. Asta e ce împiedică o pagină de pe internet să comande
serverul prin browserul tău. Fereastra desktop e recunoscută separat prin schema
ei de assets.

Nu există TLS și nu e o omisiune: unealta e pentru LAN, iar certificatele ar
însemna avertismente în browserul telefonului pentru zero câștig real.

## Probleme frecvente

### Telefonul nu ajunge la server

**Izolarea clienților (AP/client isolation)** — multe routere de consum și toate
rețelele „guest" blochează traficul între clienți wireless. Telefonul și laptopul
sunt în aceeași rețea, dar pachetele dintre ele sunt aruncate tăcut.

1. Pune laptopul pe cablu și telefonul pe Wi-Fi, sau
2. Dezactivează AP isolation în setările routerului (deseori sub
   Wireless → Advanced), sau
3. Folosește un SSID fără izolare.

### Firewall

```bash
sudo ufw allow 8080/tcp                                   # Linux
sudo iptables -I INPUT -p tcp --dport 8080 -j ACCEPT      # Linux
```

Pe macOS acceptă conexiunile când sistemul întreabă. Pe Windows permite
aplicația în Windows Defender Firewall pentru rețele private.

### „no private LAN addresses found"

Ești pe VPN sau pe o interfață publică. Deconectează VPN-ul sau conectează-te la
o rețea locală.

### Cifrele sunt mult sub așteptări

Încearcă 8 streamuri. Verifică dacă laptopul e pe Wi-Fi în loc de cablu. Deschide
„Avansat" și compară p50 cu p95: o diferență mare înseamnă o legătură instabilă,
nu una lentă.

## Ce nu e implementat

- **Mod WebRTC DataChannel** — ar da pierdere de pachete reală și jitter
  unidirecțional, lucruri pe care TCP nu le poate expune. Protocolul are un seam
  curat pentru asta, documentat în `protocol.go`; nu e implementat.
- **IPv6 link-local** — necesită zone ID (`fe80::1%en0`), pe care browserele nu îl
  acceptă în URL.
- **Mai mulți clienți simultan**, **TLS**, **rulări programate**.

## Dezvoltare

```bash
go vet ./...
go test ./...          # include un ciclu complet împotriva unui server real
go test -race ./...
go test -short ./...   # sare peste testele care mută octeți
```

## Licență

MIT — vezi [LICENSE](LICENSE).
