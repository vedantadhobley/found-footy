"""Twitter Scraper Service - Independent microservice for scraping Twitter videos

This is a standalone service that provides an API for searching Twitter videos.
It uses Selenium with Chrome for browser automation and maintains persistent 
authentication via cookies.

Architecture:
- session.py: TwitterSessionManager for browser control
- auth.py: Authentication (automated login + cookie management)
- app.py: FastAPI endpoints
- config.py: Configuration management
"""

__version__ = "1.0.0"
