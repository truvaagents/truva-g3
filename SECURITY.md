# Security Policy

## Supported Versions

Currently supported versions with security updates:

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | :white_check_mark: |

## Reporting a Vulnerability

We take the security of TruvaG3 seriously. If you believe you have found a security vulnerability, please report it to us as described below.

### Where to Report

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please report them via email to: security@truvag3.dev

You should receive a response within 48 hours. If for some reason you do not, please follow up via email to ensure we received your original message.

### What to Include

Please include the following information in your report:

- Type of issue (e.g., buffer overflow, SQL injection, cross-site scripting, etc.)
- Full paths of source file(s) related to the manifestation of the issue
- The location of the affected source code (tag/branch/commit or direct URL)
- Any special configuration required to reproduce the issue
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if possible)
- Impact of the issue, including how an attacker might exploit the issue

### What to Expect

- **Acknowledgment**: Within 48 hours of receipt
- **Initial Assessment**: Within 5 business days
- **Resolution Timeline**: Depending on severity:
  - Critical: Within 7 days
  - High: Within 14 days
  - Medium: Within 30 days
  - Low: Within 60 days

## Security Best Practices

When using TruvaG3 in production:

### 1. API Keys and Secrets

- **Never** hardcode API keys in your source code
- Use environment variables or secure secret management systems
- Rotate API keys regularly
- Use separate keys for development and production

### 2. Redis Security

- Always use Redis with authentication enabled
- Use TLS/SSL for Redis connections in production
- Configure Redis with appropriate access controls
- Regularly update Redis to the latest stable version

### 3. Network Security

- Use TLS for all agent-to-agent communication
- Implement proper network policies in Kubernetes
- Use service mesh (e.g., Istio) for additional security
- Restrict agent communication to necessary endpoints only

### 4. Input Validation

- Always validate and sanitize input from external sources
- Implement rate limiting on API endpoints
- Use timeouts for inter-agent communication
- Validate agent responses before processing

### 5. Logging and Monitoring

- Never log sensitive information (API keys, passwords, PII)
- Implement audit logging for critical operations
- Monitor for unusual agent behavior
- Set up alerts for security-related events

### 6. Container Security

- Use minimal base images for containers
- Regularly scan images for vulnerabilities
- Don't run containers as root
- Keep container dependencies up to date

## Security Features

TruvaG3 includes several security features:

- **Correlation IDs**: Track requests across the system
- **Circuit Breaker**: Prevent cascade failures
- **Health Checks**: Monitor agent health and availability
- **Structured Logging**: Secure, structured log output
- **OpenTelemetry**: Distributed tracing for security monitoring

## Disclosure Policy

When we receive a security report, we will:

1. Confirm the vulnerability
2. Determine the affected versions
3. Develop a fix
4. Release patched versions
5. Announce the vulnerability (after patches are available)

We aim to fully disclose vulnerabilities within 90 days of the initial report, or sooner if a patch is ready.

## Security Updates

Security updates will be released as:
- Patch versions for non-breaking fixes
- Minor versions if breaking changes are required
- Security advisories on GitHub

Subscribe to our security announcements:
- Watch the repository for releases
- Follow security advisories on GitHub

## Acknowledgments

We appreciate the security research community's efforts in helping keep TruvaG3 secure. Responsible disclosure of vulnerabilities helps us ensure the security and privacy of all our users.

Contributors who report valid security issues will be acknowledged in our security hall of fame (unless they prefer to remain anonymous).

## Contact

- Security Email: security@truvag3.dev
- General Inquiries: [GitHub Issues](https://github.com/truvaagents/truva-g3/issues)

## PGP Key

For sensitive communications, you may encrypt your message using our PGP key:

```
-----BEGIN PGP PUBLIC KEY BLOCK-----
[PGP KEY WOULD BE INSERTED HERE]
-----END PGP PUBLIC KEY BLOCK-----
```