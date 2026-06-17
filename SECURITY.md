# Security Policy

OpenTendril runs code execution sandboxes, executing code dynamically on your behalf. Because security is our core marketing differentiator and architectural foundation, we treat vulnerabilities with the highest priority.

---

## Supported Versions

We actively patch security vulnerabilities in the following versions of OpenTendril:

| Version | Supported |
| --- | --- |
| `< 0.1.0` (Development/Beta) | ⚠️ Security patches are backported only to `main`. Update regularly. |
| `0.1.x` (Target Stable) | ✅ Supported. Critical patches will be released immediately. |

---

## Reporting a Vulnerability

If you discover a security vulnerability—especially a **sandbox escape exploit** (breaking out of the container or gVisor runtime onto the host system), a privilege escalation bug, or an unauthorized secrets disclosure path:

1. **Do NOT open a public GitHub Issue.** Public disclosure puts local developer systems and hosted cloud platforms at risk.
2. **Email your report privately** to: **`security@opentendril.com`**
3. Include a detailed description of the vulnerability, a working Proof of Concept (PoC) or reproduction steps, and the environment under which it was tested.

### Our Disclosure Process:
* **Acknowledgment:** We will acknowledge receipt of your report within 48 hours.
* **Triage & Patching:** We will work on a patch immediately and keep you updated throughout the process.
* **Coordinated Disclosure:** We aim to release a patch and publish a public security advisory within **90 days** of receiving your report. We request that you do not disclose the vulnerability publicly until we have shipped the patch.
