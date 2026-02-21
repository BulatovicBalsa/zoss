> **Napomena 1:**  
> U svim eksperimentima korišćen je isti identitet prodavca `sellerId = s1` i proizvod `productId = p1`.  
> Razlika između zahteva bila je isključivo u polju `update` (npr. promena `price`, `title` ili njihove kombinacije).  

> **Napomena 2 (Implementaciona pojednostavljenja):**  
> Radi jednostavnosti demonstracije, svaka Worker Lambda funkcija:
> - trigerruje se isključivo putem svog odgovarajućeg SQS queue-a,
> - nema poslovnu logiku,
> - u logovima samo prikazuje sadržaj poruke koju je primila.  
>
> Na taj način, broj invokacija direktno predstavlja meru troška fan-out mehanizma.

# Worker Lambda implementacija

Svaka Worker Lambda (Cache, Indexer, Analytics) implementirana je identično:

```python
import json
import logging

logger = logging.getLogger()
logger.setLevel(logging.INFO)

def lambda_handler(event, context):
    if not isinstance(event, dict) or "Records" not in event:
        return {"status": "ok", "ignored": "non_sqs_event"}

    if not event["Records"] or event["Records"][0].get("eventSource") != "aws:sqs":
        return {"status": "ok", "ignored": "not_sqs_source"}

    for record in event["Records"]:
        sns_envelope = json.loads(record["body"])
        message = json.loads(sns_envelope["Message"])

        logger.info("Received JSON:\n%s", json.dumps(message, indent=2))

    return {"status": "ok"}
````

Na sledećoj slici prikazan je primer `CacheWorker` Lambda funkcije sa SQS trigger-om:

![CacheWorker Lambda](images/lambda-example.png)

# 1. Naivni Fan-Out pristup

## Arhitektura

![Naivna arhitektura](images/AWS-demo.png)

U ovom modelu `CatalogEvents SNS` salje event do svih subscribe-ovanih SQS redova, koji dalje pozivaju korespodentne lambda funkcije.

> Jedan publish generiše tri Lambda invokacije.

## SNS konfiguracija

![CatalogEventsNaive](images/catalog-events-naive.png)

Vidljivo je:

- Tip: Standard
- Tri aktivne SQS pretplate
- Svaka pretplata aktivira svoju Lambda funkciju
- Ne postoji nikakav mehanizam deduplikacije.

## Log demonstracija (Naivni pristup)

Poslato je više payload-ova u kratkom vremenskom periodu.

Primer uzastopnih logova:

```text
2026-02-19T17:46:50.811Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.45 }
}

2026-02-19T17:46:56.174Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.45 }
}

2026-02-19T17:47:01.327Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.44 }
}

2026-02-19T17:47:06.454Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.44 }
}
```

Ukupno: 11 poruka → 11 invokacija po workeru.

### Rezultat

|Worker|Invocations|
|---|---|
|CacheWorker|11|
|IndexerWorker|11|
|AnalyticsWorker|11|
|TOTAL|33|

# 2. SNS FIFO + Content-Based Deduplication

## SNS FIFO konfiguracija

![CatalogEventsFIFO](images/catalog-events-fifo.png)

Za razliku od prethodnog servisa, `CatalogEvents.fifo` koristi FIFO pristup sa uključenom content-based deduplikacijom.

## Bitna karakteristika

- SNS koristi hash message body-a
- Deduplikacioni prozor traje **5 minuta**
- Trajanje prozora **nije moguće menjati**

## Log demonstracija (FIFO)

Primeri primljenih poruka sa vremenskim oznakama:

```text
2026-02-20T09:24:35.583Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.45 }
}

2026-02-20T09:25:07.200Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.44 }
}

2026-02-20T09:25:44.375Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123 }
}

2026-02-20T09:35:33.005Z
Received JSON:
{
  "sellerId": "s1",
  "productId": "p1",
  "update": { "price": 123.45 }
}
```

Uočeno:

- `price: 123.45` pojavljuje se dva puta.
- Razmak između prvog i poslednjeg događaja je veći od 5 minuta.
- Tokom 5-minutnog prozora identična poruka nije prosleđena.
- Minimalna promena vrednosti (`123.45 → 123.44`) tretira se kao nova poruka.

### Rezultat

|Worker|Invocations|
|---|---|
|CacheWorker|6|
|IndexerWorker|6|
|AnalyticsWorker|6|

Ukupno: **18 invokacija**

> FIFO smanjuje trošak, ali ga ne eliminiše kod varijacija payload-a.

# 3. Agregacija (Digest pristup)

## Arhitektura sa agregacijom

![Agregirana arhitektura](images/Solution.png)

### Dodate komponente

**1. Aggregator Lambda**  
Svaki događaj koji prođe SNS FIFO upisuje se u DynamoDB tabelu radi privremene agregacije.

**2. DynamoDB tabela**  
Služi kao privremeno skladište promena po ključu `(sellerId, productId)`.

**3. Flush Timer (EventBridge – 5 minuta)**  
Periodički aktivira Flusher Lambda funkciju.

**4. Flusher Lambda**  
Čita agregirane promene iz DynamoDB, formira objedinjeni događaj i šalje ga nazad na SNS radi fan-out distribucije.

## Rezultat agregacije

Od 11 zahteva:

- U agregator je dospelo 4
- Promene su objedinjene u jedan zapis
- Ka worker-ima je poslat samo jedan agregirani događaj

Primer:

```json
{
  "sellerId": "s1",
  "productId": "p1",
  "update": {
    "title": "New title",
    "price": 123.45
  }
}
```

### Invokacije

|Worker|Invocations|
|---|---|
|CacheWorker|1|
|IndexerWorker|1|
|AnalyticsWorker|1|
|Aggregator|4|

Ukupno: **7 invokacija**

---

# 4. Poređenje potrošnje

## Tabelarni prikaz

| Worker           | Naive | FIFO | Digest |
| ---------------- | ----- | ---- | ------ |
| AnalyticsWorker  | 11    | 6    | 1      |
| CacheWorker      | 11    | 6    | 1      |
| IndexerWorker    | 11    | 6    | 1      |
| Aggregator       | 0     | 0    | 4      |
| CatalogEventsSNS | 11    | 11   | 11     |

## Ukupan broj Lambda invokacija

| Scenario | Total |
| -------- | ----- |
| Naive    | 33    |
| FIFO     | 18    |
| Digest   | 7     |

## Vizuelni prikaz

![Chart](images/chart.png)