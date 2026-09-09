import { useState } from "react";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

type ChatImageProps = {
  src?: string;
  alt?: string;
  title?: string;
};

export function ChatImage({ src, alt = "", title }: ChatImageProps) {
  const [open, setOpen] = useState(false);
  const [ratio, setRatio] = useState<number>();
  const [failed, setFailed] = useState(false);
  if (!src) return <span>{alt || "Image unavailable"}</span>;
  const loaded = (image: HTMLImageElement) => {
    if (image.naturalWidth && image.naturalHeight) {
      setRatio(image.naturalWidth / image.naturalHeight);
      setFailed(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {/* Keep the shadow inside the message item's paint-containment boundary. */}
      <DialogTrigger
        render={
          <Button
            type="button"
            variant="ghost"
            className="h-auto min-w-0 max-w-full cursor-zoom-in p-4 align-middle"
            aria-label={alt ? `Open image: ${alt}` : "Open image"}
          />
        }
      >
        <img
          src={src}
          alt={alt}
          title={title}
          loading="lazy"
          referrerPolicy="no-referrer"
          className="max-h-96 max-w-full rounded-lg border border-border shadow-lg"
          onLoad={(event) => loaded(event.currentTarget)}
          onError={() => setFailed(true)}
        />
      </DialogTrigger>
      <DialogContent
        showCloseButton={false}
        overlayClassName="bg-black/80"
        className="flex h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] max-w-none items-center justify-center rounded-none bg-transparent p-0 shadow-none ring-0 sm:max-w-none"
        onClick={(event) => {
          // The space around the image is also part of the darkened backdrop.
          if (event.target === event.currentTarget) setOpen(false);
        }}
      >
        <DialogTitle className="sr-only">Image preview</DialogTitle>
        <DialogDescription className="sr-only">
          {alt || "Enlarged chat image"}. Press Escape or use Close to return to
          the chat.
        </DialogDescription>
        {failed ? (
          <p role="alert" className="text-sm text-white">
            This image could not be loaded.
          </p>
        ) : (
          <>
            {!ratio && (
              <p role="status" className="text-sm text-white">
                Loading image…
              </p>
            )}
            <img
              src={src}
              alt={alt}
              referrerPolicy="no-referrer"
              className={cn(
                "max-h-[calc(100dvh-6rem)] max-w-full object-contain shadow-2xl",
                !ratio && "absolute invisible",
              )}
              // Size to the available viewport while preserving the actual image
              // bounds, so clicking the surrounding letterbox area dismisses it.
              style={
                ratio
                  ? {
                      width: `min(calc(100vw - 2rem), calc((100dvh - 6rem) * ${ratio}))`,
                    }
                  : undefined
              }
              onLoad={(event) => loaded(event.currentTarget)}
              onError={() => setFailed(true)}
            />
          </>
        )}
        <DialogClose
          render={
            <Button
              variant="secondary"
              size="icon"
              className="absolute right-0 top-0 shadow-md"
              aria-label="Close image preview"
            />
          }
        >
          <X />
        </DialogClose>
      </DialogContent>
    </Dialog>
  );
}
