# GoLoadTestingApp

YAML senaryolarla HTTP yük testi yapan Go aracı. Plan: Faz 0 (senaryo formatı) ve Faz 1 (tek node engine + CLI) tamam.

## Kullanım

```bash
make build
./bin/goloadtest validate examples/smoke.yaml
./bin/goloadtest run examples/smoke.yaml
./bin/goloadtest run -quiet -out result.json examples/spike.yaml
```

Exit code: `0` başarılı, `1` threshold başarısız, `2` senaryo/kullanım hatası, `3` run hatası. CI'da doğrudan kullanılabilir.

## Senaryo formatı

`internal/scenario/types.go` formatın tek kaynağıdır. Örnekler `examples/` altında:

- `smoke.yaml`: open model, basit GET'ler, threshold'lar
- `login_flow.yaml`: closed model, CSV kullanıcılar, token extract
- `spike.yaml`: ramp + ani yük sıçraması

## Mimari

| Paket | Sorumluluk |
|---|---|
| `internal/scenario` | YAML parse, default'lar, validation, `{{var}}` render |
| `internal/data` | CSV feeder (sequential / random / unique) |
| `internal/engine` | Open model (arrival rate, analitik schedule) ve closed model (VU) |
| `internal/protocol/httpx` | HTTP executor: extract, assert, body pooling |
| `internal/metrics` | Sharded HDR histogram collector, merge, threshold |
| `cmd/goloadtest` | CLI |

Ölçüm doğruluğu kuralları:

- **Coordinated omission:** latency, isteğin planlanan gönderim anından ölçülür.
- **Percentile:** histogramlar merge edilir, percentile ortalaması alınmaz.
- **Varsayılan hata:** status assertion'ı olmayan step'te HTTP 400 ve üstü hata sayılır.
- **Drop:** `max_vus` dolunca open model'de gelen iterasyon drop edilir ve sayılır.
- **CPU uyarısı:** yük üreten makinenin CPU kullanımı %80'i aşarsa run sonunda uyarı basılır.
