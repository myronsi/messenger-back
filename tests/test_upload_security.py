"""Uploads are validated by content, not extension (stored XSS hardening)."""
import io
import unittest

from fastapi import HTTPException

from server.upload_security import (
    content_matches_extension,
    ensure_inline_content_is_genuine,
    process_avatar,
    safe_filename,
    sniff_mime,
)


def make_image(fmt, size=(40, 20), mode="RGB"):
    from PIL import Image

    output = io.BytesIO()
    Image.new(mode, size, "red").save(output, format=fmt)
    return output.getvalue()


class SniffTests(unittest.TestCase):
    def test_detects_supported_media(self):
        self.assertEqual(sniff_mime(make_image("PNG")[:32]), "image/png")
        self.assertEqual(sniff_mime(make_image("JPEG")[:32]), "image/jpeg")
        self.assertEqual(sniff_mime(make_image("GIF")[:32]), "image/gif")
        self.assertEqual(sniff_mime(make_image("WEBP")[:32]), "image/webp")
        self.assertEqual(sniff_mime(b"%PDF-1.7"), "application/pdf")
        self.assertEqual(sniff_mime(b"OggS\x00\x02"), "audio/ogg")
        self.assertEqual(sniff_mime(b"\x1a\x45\xdf\xa3\x01"), "video/webm")
        self.assertEqual(sniff_mime(b"\x00\x00\x00\x20ftypisom"), "video/mp4")
        self.assertEqual(sniff_mime(b"ID3\x04"), "audio/mpeg")

    def test_active_content_is_not_media(self):
        for body in (b"<!doctype html><script>1</script>", b"<svg xmlns='http://www.w3.org/2000/svg'/>", b"alert(1)", b""):
            self.assertIsNone(sniff_mime(body))

    def test_extension_must_match_content(self):
        html = b"<html><script>alert(1)</script></html>"
        for extension in (".png", ".jpg", ".gif", ".webp", ".pdf", ".mp4", ".mp3", ".webm", ".ogg"):
            self.assertFalse(content_matches_extension(extension, html), extension)
            with self.assertRaises(HTTPException):
                ensure_inline_content_is_genuine(extension, html)
        # An image renamed to a different media extension is rejected too
        self.assertFalse(content_matches_extension(".pdf", make_image("PNG")[:32]))
        # Extensions that are never rendered inline are not checked (they are served as downloads)
        self.assertTrue(content_matches_extension(".html", html))
        self.assertTrue(content_matches_extension(".png", make_image("PNG")[:32]))

    def test_safe_filename_strips_directories(self):
        self.assertEqual(safe_filename("../../etc/passwd"), "passwd")
        self.assertEqual(safe_filename("..\\evil.html"), "evil.html")
        self.assertEqual(safe_filename(None), "file")
        self.assertEqual(safe_filename(".."), "file")


class AvatarTests(unittest.TestCase):
    def test_reencodes_images(self):
        data, extension = process_avatar(make_image("PNG"))
        self.assertEqual(extension, ".jpg")
        self.assertEqual(sniff_mime(data[:32]), "image/jpeg")

    def test_keeps_transparency_as_png(self):
        data, extension = process_avatar(make_image("PNG", mode="RGBA"))
        self.assertEqual(extension, ".png")

    def test_large_images_are_downscaled(self):
        from PIL import Image

        data, _ = process_avatar(make_image("PNG", size=(3000, 1500)))
        self.assertEqual(max(Image.open(io.BytesIO(data)).size), 1024)

    def test_metadata_payloads_are_dropped(self):
        from PIL import Image

        image = Image.new("RGB", (10, 10))
        output = io.BytesIO()
        image.save(output, format="PNG", pnginfo=_text_chunk("<script>alert(1)</script>"))
        data, _ = process_avatar(output.getvalue())
        self.assertNotIn(b"<script>", data)

    def test_rejects_images_over_the_pixel_cap(self):
        from PIL import Image

        output = io.BytesIO()
        Image.new("1", (5000, 4001)).save(output, format="PNG")
        with self.assertRaises(HTTPException) as raised:
            process_avatar(output.getvalue())
        self.assertEqual(raised.exception.detail, "Avatar dimensions are too large")

    def test_rejects_non_images_and_oversized_files(self):
        for body in (b"<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>", b"<html></html>", b"", b"\x89PNG\r\n\x1a\nnot really"):
            with self.assertRaises(HTTPException) as raised:
                process_avatar(body)
            self.assertEqual(raised.exception.status_code, 400)
        with self.assertRaises(HTTPException):
            process_avatar(make_image("PNG") + b"0" * (5 * 1024 * 1024))


def _text_chunk(text):
    from PIL.PngImagePlugin import PngInfo

    info = PngInfo()
    info.add_text("payload", text)
    return info


if __name__ == "__main__":
    unittest.main()
