import { HlmButtonImports } from '@spartan-ng/helm/button';
import { Component, ElementRef, input, output, signal, viewChild } from '@angular/core';

/** A signature drawn with a finger, pen or mouse, saved as a PNG image. */
@Component({
  selector: 'fimra-signature-pad',
  imports: [HlmButtonImports],
  host: { class: 'signature-pad' },
  template: `
    <canvas #pad width="480" height="160" [id]="inputId()" role="img" tabindex="0"
      [attr.aria-describedby]="describedBy()"
      [attr.aria-label]="drawn() ? 'Signature drawn. Save it or clear it.' : 'Signature area. Draw your signature here.'"
      (pointerdown)="start($event)" (pointermove)="move($event)" (pointerup)="end()" (pointerleave)="end()"></canvas>
    <div class="shape-actions">
      <button hlmBtn type="button" size="sm" [disabled]="!drawn() || busy()" (click)="save()">Save signature</button>
      <button hlmBtn type="button" variant="ghost" size="sm" [disabled]="!drawn() || busy()" (click)="clear()">Clear</button>
    </div>
  `
})
export class SignaturePad {
  readonly inputId = input.required<string>();
  readonly describedBy = input<string | null>(null);
  readonly busy = input(false);
  readonly saved = output<Blob>();
  readonly drawn = signal(false);
  private readonly pad = viewChild.required<ElementRef<HTMLCanvasElement>>('pad');
  private drawing = false;

  private context(): CanvasRenderingContext2D | null {
    return this.pad().nativeElement.getContext('2d');
  }

  private point(event: PointerEvent): [number, number] {
    const canvas = this.pad().nativeElement;
    const box = canvas.getBoundingClientRect();
    return [(event.clientX - box.left) * (canvas.width / (box.width || 1)), (event.clientY - box.top) * (canvas.height / (box.height || 1))];
  }

  start(event: PointerEvent): void {
    const ctx = this.context();
    if (!ctx) return;
    this.drawing = true;
    this.pad().nativeElement.setPointerCapture?.(event.pointerId);
    ctx.lineWidth = 2.5;
    ctx.lineCap = 'round';
    // The ink takes the theme's text colour from the stylesheet.
    ctx.strokeStyle = getComputedStyle(this.pad().nativeElement).color;
    ctx.beginPath();
    ctx.moveTo(...this.point(event));
  }

  move(event: PointerEvent): void {
    const ctx = this.context();
    if (!this.drawing || !ctx) return;
    ctx.lineTo(...this.point(event));
    ctx.stroke();
    this.drawn.set(true);
  }

  end(): void {
    this.drawing = false;
  }

  clear(): void {
    const canvas = this.pad().nativeElement;
    this.context()?.clearRect(0, 0, canvas.width, canvas.height);
    this.drawn.set(false);
  }

  save(): void {
    // Saved on the card colour, so the signature reads on any background.
    const canvas = this.pad().nativeElement;
    const copy = document.createElement('canvas');
    copy.width = canvas.width;
    copy.height = canvas.height;
    const ctx = copy.getContext('2d');
    if (!ctx) return;
    ctx.fillStyle = getComputedStyle(canvas).backgroundColor;
    ctx.fillRect(0, 0, copy.width, copy.height);
    ctx.drawImage(canvas, 0, 0);
    copy.toBlob(blob => { if (blob) this.saved.emit(blob); }, 'image/png');
  }
}
