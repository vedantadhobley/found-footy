"""Centralized logging utilities for Found Footy"""
import logging
import traceback
from typing import Optional

def get_logger(name: str, level: int = logging.INFO) -> logging.Logger:
    logger = logging.getLogger(name)
    logger.setLevel(level)
    # DO NOT add a handler here!
    return logger

def log_error_with_trace(logger: logging.Logger, message: str, exception: Exception) -> None:
    logger.error(f"{message}: {exception}")
    logger.error(f"Traceback: {traceback.format_exc()}")