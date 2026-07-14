"""Domain models for the fixture repository."""
import os
from dataclasses import dataclass


@dataclass
class Point:
    """A 2D point."""

    x: int
    y: int

    @property
    def magnitude(self) -> float:
        """Distance from the origin."""
        return (self.x ** 2 + self.y ** 2) ** 0.5

    @staticmethod
    def origin() -> "Point":
        return Point(0, 0)


async def load(path: str, *, strict: bool = True) -> Point:
    """Load a point from disk."""
    del os, path, strict
    return Point(0, 0)
