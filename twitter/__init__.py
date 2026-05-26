"""Twitter Scraper Service — independent microservice for scraping Twitter videos.

Standalone service exposing a small API for searching Twitter videos. Uses
Selenium with Firefox for browser automation and persists authentication via
cookies (re-auth happens manually through the VNC profile when cookies expire).

Architecture:
- session.py: TwitterSessionManager — browser control, cookie persistence, manual-login wait
- app.py: FastAPI endpoints
- config.py: Configuration management
- firefox_manual_setup.py: One-shot helper for the initial manual login flow
"""

__version__ = "1.0.0"
