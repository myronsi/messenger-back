"""Dimensions and thumbnails for uploaded chat images.

Clients use the stored size to reserve space before an image loads and the thumbnail to avoid downloading
the full file for the message list. Anything that cannot be decoded safely simply gets no metadata.
"""
import io

IMAGE_EXTENSIONS = frozenset({".jpg", ".jpeg", ".png", ".gif", ".webp"})
IMAGE_METADATA_KEYS = ("image_width", "image_height", "thumbnail_url")
THUMBNAIL_MAX_SIDE = 640
# About 80 MB as RGBA when decoded; larger images get no metadata instead of tying up a worker.
MAX_DECODED_PIXELS = 20_000_000


def describe_image(content: bytes, extension: str) -> tuple[dict, bytes | None]:
    """Return ({"image_width", "image_height"}, thumbnail JPEG/PNG bytes or None) for an image upload."""
    if extension.lower() not in IMAGE_EXTENSIONS:
        return {}, None

    from PIL import Image, ImageOps, UnidentifiedImageError

    try:
        with Image.open(io.BytesIO(content)) as image:
            width, height = image.size
            if width < 1 or height < 1 or width * height > MAX_DECODED_PIXELS:
                return {}, None
            animated = bool(getattr(image, "is_animated", False))
            image.seek(0)
            oriented = ImageOps.exif_transpose(image)
            if oriented.size != (width, height):
                width, height = oriented.size
            metadata = {"image_width": width, "image_height": height}
            # Animations keep their original file, and small images are already cheap.
            if animated or max(width, height) <= THUMBNAIL_MAX_SIDE:
                return metadata, None
            oriented.thumbnail((THUMBNAIL_MAX_SIDE, THUMBNAIL_MAX_SIDE))
            has_alpha = oriented.mode in ("RGBA", "LA", "PA") or "transparency" in oriented.info
            output = io.BytesIO()
            if has_alpha:
                oriented.convert("RGBA").save(output, format="PNG", optimize=True)
            else:
                oriented.convert("RGB").save(output, format="JPEG", quality=80, optimize=True)
            thumbnail = output.getvalue()
            return metadata, thumbnail if len(thumbnail) < len(content) else None
    except (UnidentifiedImageError, OSError, ValueError, Image.DecompressionBombError):
        return {}, None


def thumbnail_extension(thumbnail: bytes) -> str:
    return ".png" if thumbnail.startswith(b"\x89PNG") else ".jpg"