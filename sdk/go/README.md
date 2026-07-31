# trident-go-sdk

Go client SDK for the [Trident](https://github.com/Telocel-Labs/Trident) Soroban event indexer.

## Regenerating OpenAPI models

Install the generator dependency once with `python3 -m pip install PyYAML`, then run:

```bash
python3 scripts/generate_sdk_models.py --language go
```

Generated models live in `github.com/Depo-dev/trident/sdk/go/openapi`.
