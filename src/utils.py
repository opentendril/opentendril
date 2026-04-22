import html

def safe(text: str) -> str:
    """Escape text for safe HTML rendering."""
    return html.escape(str(text))
