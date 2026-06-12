# Bundled country data — attribution & license

`countries.json` in this directory is the **mledoze/countries** dataset,
embedded into the `country-info-tool` binary via `go:embed` and used for
offline country lookup (name/ISO-code matching plus capital, region,
languages, currency, flag emoji, etc.).

- **Source:** https://github.com/mledoze/countries
- **Pinned commit:** `09b28e3d03e6ca3fbbac996d716a50d929781e8c`
  (file fetched verbatim from `countries.json` at that commit — unmodified)
- **License:** Open Data Commons Open Database License (ODbL) v1.0 — see
  [`LICENSE`](./LICENSE) in this directory.

## ODbL compliance notes

- The dataset is redistributed **unmodified**; its license text is shipped
  alongside it (`data/LICENSE`) and this attribution is retained.
- Population and timezone values are **not** taken from this dataset (it does
  not provide them); they are fetched at runtime from
  [apicountries.com](https://apicountries.com/) by exact ISO code.
- Per ODbL, output produced from the database (the tool's JSON responses) is a
  "Produced Work" and is not itself subject to the share-alike terms; the
  database and any derivative databases remain under ODbL.

To update the dataset, re-fetch `countries.json` from a newer mledoze commit
and update the pinned commit hash above.
