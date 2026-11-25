import { Component, ElementRef, effect, inject, input, viewChild } from '@angular/core';
import { BarChart } from 'echarts/charts';
import { GridComponent } from 'echarts/components';
import { init, use } from 'echarts/core';
import { SVGRenderer } from 'echarts/renderers';
import tokens from '../../../docs/design-system/tokens.json';
import { classes, ForestClass } from './detections';
import { ThemeService } from './theme';

use([BarChart, GridComponent, SVGRenderer]);

@Component({selector: 'fimra-class-chart', template: '<div #plot class="class-chart" aria-hidden="true"></div>'})
export class ClassChart {
  readonly counts = input.required<Partial<Record<ForestClass, number>>>();
  private readonly plot = viewChild<ElementRef<HTMLDivElement>>('plot');
  private readonly appearance = inject(ThemeService);

  constructor() {
    effect(onCleanup => {
      const el = this.plot()?.nativeElement;
      if (!el) return;
      const theme = this.appearance.theme();
      const data = this.counts();
      const keys = Object.keys(classes) as ForestClass[];
      const chart = init(el, undefined, {renderer: 'svg'});
      const palette = tokens.color[theme];
      chart.setOption({
        animation: false,
        grid: {left: 160, right: 32, top: 12, bottom: 24},
        yAxis: {type: 'category', inverse: true, data: keys.map(k => classes[k]),
          axisLabel: {color: palette.text, fontSize: 11}, axisTick: {show: false}},
        xAxis: {type: 'value', minInterval: 1, axisLabel: {color: palette.textSecondary},
          splitLine: {lineStyle: {color: palette.border}}},
        series: [{type: 'bar', barMaxWidth: 24, label: {show: true, position: 'right', color: palette.text},
          data: keys.map((key, index) => ({value: data[key] ?? 0, itemStyle: {color: tokens.color.chart[theme][index]}}))}]
      });
      const observer = new ResizeObserver(() => chart.resize());
      observer.observe(el);
      onCleanup(() => { observer.disconnect(); chart.dispose(); });
    });
  }
}
