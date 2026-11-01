# Notes

- **Clone prevention is weak.** Identity of a Pokemon is the SHA-256 of the stored bytes per account. A
  tweaked copy of a file gets in twice. Proper identity (EC + PID + OT) needs `pokemon-legality`'s `/v1/check`
  to return them. Follow-up on that service.
- **Judge** needs IVs in the verdict; same follow-up.
- **GTS generation limit** comes from global-link: a trade only matches within one generation.
- **Unverified files**: a Pokemon handed over by global-link that this service's own gate then refuses (or
  cannot reach) is parked in limbo as `unverified` and only enters a box after passing the gate.
- **Wonder Box ownership** changes only when the receiving player collects, so nothing moves if they never return.
- **Box counts** are per deployment (`HOME_BOXES`); HOME's Basic/Premium plans are not modelled.
- **Pokemon Bank (3DS)** is a separate 3DS service; an import endpoint for Bank-derived PK7 files would go
  through the same gate. Not built.
- **PKH** encryption helper follows PKHeX's documented layout and is untested against a real HOME file.
