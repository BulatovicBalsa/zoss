# Threat Decomposition Tree

## 1. Ekonomska održivost sistema

### Pretnja: Asymmetric Cost Exploitation (ACE)

- [ACE kroz Event Fan-Out arhitekturu](documentation/Asymmetric%20Cost%20Exploitation%20kroz%20Event%20Fan-out%20Arhitekturu%20Sistema.md)

  - SNS FIFO sa content-based deduplikacijom
  - Agregacija događaja pre fan-out distribucije

- [ACE kroz Email Notification Fan-Out](documentation/Asymmetric%20Cost%20Exploitation%20kroz%20Email%20Notifikacije.md)

  - Agregacija i digest notifikacije
  - Rate limiting po pretplatniku
  - Diferencijacija i verifikacija pretplatnika

- [ACE kroz S3 Storage Amplification](documentation/Asymmetric%20Cost%20Exploitation%20kroz%20S3%20Storage%20Amplification.md)

  - Kvota po nalogu
  - Ograničenje veličine objekta
  - Lifecycle politika za automatsko brisanje

- [ACE kroz Multipart Upload Abandonment](documentation/Asymmetric%20Cost%20Exploitation%20kroz%20Multipart%20Upload%20Abandonment.md)

  - Lifecycle politika za abortiranje nepotpunih upload-a
  - Ograničavanje maksimalne veličine objekta

---

## 2. Skladišni sloj sistema

### Pretnja: Neautorizovan ili nekontrolisan pristup skladišnim resursima sistema

- [Direct Bucket Write](documentation/Direct%20Bucket%20Write%20napad%20i%20mitigacija%20primenom%20pre-signed%20URL%20mehanizma.md)

  - Pre-Signed URL mehanizam
