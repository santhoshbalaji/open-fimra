import { Component } from '@angular/core';
import { translate } from './language';

@Component({
  selector: 'fimra-meteosat-attribution',
  template: `
    <p class="text-label text-text-secondary">
      {{ msg('EUMETSAT LSA SAF · MSG-IODC FRP-PIXEL (Demonstration)') }} ·
      <a class="underline" href="https://creativecommons.org/licenses/by/4.0/">CC BY 4.0</a> ·
      <a class="underline" href="https://doi.org/10.5194/acp-15-13217-2015">Wooster et al. (2015)</a>
    </p>
  `
})
export class MeteosatAttribution {
  protected readonly msg = translate;
}
