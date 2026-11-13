---
type: security
---
Updated openssh from 10.3_p1-r1 to 10.3_p1-r2, which fixes CVE-2026-123158 that scanners rate critical, https://github.com/coollabsio/coolify-helper/issues/1

- The CVE is in `sshd`, which the image does not ship, so it was not exploitable in the image
