# trident-indexer

Python client SDK for the [Trident](https://github.com/Telocel-Labs/Trident) Soroban event indexer.

```
pip install trident-indexer
```

## Sync usage

```python
from trident_indexer import TridentClient

client = TridentClient(
    api_url="https://api.trident.example.com",
    api_key="your-api-key",
    network="mainnet",
)

# Query events
page = client.query_events(contract_id="CABC...", topic_0="transfer", limit=10)
for event in page.events:
    print(event.id, event.data)

# Fetch a single event
event = client.get_event_by_id("550e8400-e29b-41d4-a716-446655440000")

# Real-time subscription
handle = client.subscribe_to_contract("CABC...", on_event=lambda e: print(e))
# ... later:
handle.close()
```

## Async usage

```python
import asyncio
from trident_indexer import AsyncTridentClient

async def main():
    async with AsyncTridentClient(
        api_url="https://api.trident.example.com",
        api_key="your-api-key",
    ) as client:
        page = await client.query_events(contract_id="CABC...")
        event = await client.get_event_by_id("550e8400-...")

        # Async generator for real-time events
        async for event in client.iter_events("CABC..."):
            print(event)

asyncio.run(main())
```

## Error handling

```python
from trident_indexer import TridentApiError

try:
    event = client.get_event_by_id("missing-id")
except TridentApiError as e:
    print(e.status, e.code, str(e))  # 404 NOT_FOUND event not found
```
