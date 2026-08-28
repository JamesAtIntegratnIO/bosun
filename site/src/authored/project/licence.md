---
title: Licence and use
description: PolyForm Internal Use 1.0.0 — what you may do without asking, what needs a separate licence, and why the line is drawn where it is.
---

Bosun is licensed under
[PolyForm Internal Use 1.0.0](https://github.com/JamesAtIntegratnIO/bosun/blob/main/LICENSE).

This page is a plain-language summary. The licence text is the authority; where
they disagree, the licence wins.

## You may, without asking anyone

**Run it for your own business, commercially, in production.** There is no seat
count, no revenue threshold, no evaluation period and nothing to sign. If Kargo
is opening pull requests you have not got time to read, this is for you and
there is nothing to negotiate.

That includes:

- Installing the chart and pulling the image from the registry. **This is use,
  not distribution.**
- Running it against private repositories and production clusters.
- Modifying it for your own internal use.
- Running it inside a company of any size.

## You may not

**Distribute it.** Not sold, not bundled into a product, not offered as a hosted
service, not handed on.

This is stricter than "do not sell it" — it rules out free redistribution too:

| | |
|---|---|
| Public forks | Not permitted |
| Bundling it into something you ship | Not permitted |
| Running it on a client's behalf as a service | Not permitted |
| Offering it as SaaS | Not permitted |
| Republishing the image or chart | Not permitted |

Providing it to third parties in any form needs a **separate licence**. Ask —
that is the intended path, not a closed door.

## Why this licence

The gate is a security control. A fork that quietly weakens a blocking rule and
keeps the name is a worse outcome than no gate at all, because the failure looks
exactly like success — which is the same argument the
[safety model](/concepts/safety-model/) makes about a model that can edit its
own checks.

Internal use is unrestricted precisely because that risk does not apply there:
you know what you changed.

## Contributing

Contributions are welcome under the same terms. See
[Contributing](/project/contributing/) for the toolchain and what a change has
to prove before it lands.

## Asking

Open an issue on
[the repository](https://github.com/JamesAtIntegratnIO/bosun/issues), or get in
touch directly. Distribution licences are granted; they just have to be asked
for.
