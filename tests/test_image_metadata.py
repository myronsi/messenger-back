"""Image dimensions and thumbnails recorded for chat uploads."""
import io
import unittest

from PIL import Image

from server.image_metadata import IMAGE_WORK_CONCURRENCY, describe_image, run_image_work, thumbnail_extension
from tests.test_upload_security import make_image


class DescribeImageTests(unittest.TestCase):
    def test_small_image_reports_size_without_thumbnail(self):
        metadata, thumbnail = describe_image(make_image("PNG", size=(40, 20)), ".png")
        self.assertEqual(metadata, {"image_width": 40, "image_height": 20})
        self.assertIsNone(thumbnail)

    def test_large_image_gets_a_smaller_thumbnail(self):
        body = make_image("JPEG", size=(2000, 1000))
        metadata, thumbnail = describe_image(body, ".jpg")
        self.assertEqual(metadata, {"image_width": 2000, "image_height": 1000})
        self.assertIsNotNone(thumbnail)
        self.assertLess(len(thumbnail), len(body))
        with Image.open(io.BytesIO(thumbnail)) as image:
            self.assertEqual(max(image.size), 640)
            self.assertAlmostEqual(image.size[0] / image.size[1], 2.0, places=1)
        self.assertEqual(thumbnail_extension(thumbnail), ".jpg")

    def test_transparent_image_keeps_alpha_in_a_png_thumbnail(self):
        _, thumbnail = describe_image(make_image("PNG", size=(1600, 1600), mode="RGBA"), ".png")
        self.assertIsNotNone(thumbnail)
        self.assertEqual(thumbnail_extension(thumbnail), ".png")

    def test_exif_orientation_swaps_reported_dimensions(self):
        image = Image.new("RGB", (1200, 600), "blue")
        exif = Image.Exif()
        exif[0x0112] = 6
        output = io.BytesIO()
        image.save(output, format="JPEG", exif=exif)
        metadata, _ = describe_image(output.getvalue(), ".jpg")
        self.assertEqual(metadata, {"image_width": 600, "image_height": 1200})

    def test_animated_gif_is_not_thumbnailed(self):
        frames = [Image.new("RGB", (900, 900), color) for color in ("red", "green")]
        output = io.BytesIO()
        frames[0].save(output, format="GIF", save_all=True, append_images=frames[1:])
        metadata, thumbnail = describe_image(output.getvalue(), ".gif")
        self.assertEqual(metadata["image_width"], 900)
        self.assertIsNone(thumbnail)

    def test_images_over_the_pixel_cap_are_not_decoded(self):
        output = io.BytesIO()
        Image.new("1", (5000, 4001)).save(output, format="PNG")
        self.assertEqual(describe_image(output.getvalue(), ".png"), ({}, None))

    def test_non_images_and_garbage_get_no_metadata(self):
        self.assertEqual(describe_image(b"%PDF-1.7", ".pdf"), ({}, None))
        self.assertEqual(describe_image(b"not an image", ".png"), ({}, None))


class ImageWorkTests(unittest.TestCase):
    def test_image_work_runs_off_the_event_loop_with_bounded_concurrency(self):
        import asyncio
        import threading
        import time

        lock = threading.Lock()
        active = 0
        peak = 0
        loop_threads = []

        def decode():
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
            time.sleep(0.05)
            with lock:
                active -= 1
            return threading.current_thread()

        async def main():
            loop_threads.append(threading.current_thread())
            return await asyncio.gather(*(run_image_work(decode) for _ in range(IMAGE_WORK_CONCURRENCY * 3)))

        worker_threads = asyncio.run(main())
        self.assertLessEqual(peak, IMAGE_WORK_CONCURRENCY)
        self.assertNotIn(loop_threads[0], worker_threads)


if __name__ == "__main__":
    unittest.main()
