# Mainnet SAC Asset Reference List

Reference list of Stellar Asset Contracts (SAC) to track at mainnet launch via
`TRACKED_SAC_ASSETS`. Contract ids were cross-checked against the
[stellar.expert](https://stellar.expert/explorer/public) public asset registry
(2026-10-01). Re-verify before go-live (see below) — this list is a launch input
that must be reviewed and signed off, not blindly copied.

| Asset | Issuer | SAC contract id |
|-------|--------|-----------------|
| XLM (native) | — | `CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA` |
| USDC (Circle) | `GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN` | `CCW67TSZV3SSS2HXMBQ5JFGCKJNXKZM7UQUWUZPUTHXSTZLEO7SJMI75` |
| EURC (Circle) | `GDHU6WRG4IEQXM5NZ4BMPKOXHW76MZM4Y2IEMFDVXBSDP6SJY4ITNPP2` | `CDTKPWPLOURQA2SGTKTUQOWRCBZEORB4BWBOMJ3D3ZTQQSGE5F6JBQLV` |

## Ready-to-use value

```env
NETWORK=mainnet
TRACKED_SAC_ASSETS=native,USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN,EURC:GDHU6WRG4IEQXM5NZ4BMPKOXHW76MZM4Y2IEMFDVXBSDP6SJY4ITNPP2
```

`TRACKED_SAC_ASSETS` takes `CODE:ISSUER` entries; the indexer derives each SAC
contract id from the issuer and the mainnet passphrase
(`Public Global Stellar Network ; September 2015`). The derived id must equal the
table above — if it does not, `NETWORK`/`NETWORK_PASSPHRASE` is wrong.

## Verification procedure (do before go-live)

1. Derive each id independently with the Stellar CLI:
   `stellar contract id asset --asset USDC:GA5Z... --network mainnet`
   (use `--asset native` for XLM) and compare with the table.
2. Confirm each issuer account on stellar.expert matches the issuer published by
   the asset's operator (Circle for USDC/EURC).
3. Record the reviewer and date in the launch checklist
   ([LAUNCH_CHECKLIST.md](LAUNCH_CHECKLIST.md)).

Adding assets beyond this list is a deliberate, reviewed change: add a row here
with the verified contract id first.
