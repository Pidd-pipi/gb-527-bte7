import { ChangeDetectionStrategy, Component, EventEmitter, Input, Output } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ConflictResolution, ResolutionSuggestion } from '../../types/conflict';
import { formatUtc } from '../../utils/date';

const MICRO_TIME_SHIFT = 'micro_time_shift';

@Component({
  selector: 'app-resolution-compare-panel',
  standalone: true,
  imports: [CommonModule],
  template: `
    <div class="compare" *ngIf="resolution">
      <button *ngFor="let suggestion of resolution.suggestions; let index = index" type="button" class="suggestion"
        [class.selected]="selectedKey === suggestion.action_key" [disabled]="readonly" (click)="choose(suggestion)">
        <span class="rank">{{ index + 1 }}</span>
        <span class="body"><strong>{{ suggestion.title }}</strong><small>{{ suggestion.rationale }}</small>
          <div class="shift" *ngIf="suggestion.action_type === microShiftType">
            <b>{{ shiftLabel(suggestion.shift_minutes ?? 0) }}</b>
            <span class="utc old"><i>before</i>{{ interval(suggestion.original_start_at, suggestion.original_end_at) }}</span>
            <span class="utc new"><i>after</i>{{ interval(suggestion.shifted_start_at, suggestion.shifted_end_at) }}</span>
          </div>
          <span class="tags"><i>score {{ suggestion.score.total_score | number:'1.2-2' }}</i><i>loss {{ suggestion.score.priority_loss }}</i><i>{{ suggestion.score.contact_duration_sec }} sec</i><i>margin {{ suggestion.score.resource_margin }}</i><i *ngIf="suggestion.requires_manual">manual</i></span>
        </span>
        <span class="choice">{{ selectedKey === suggestion.action_key ? 'SELECTED' : 'SELECT' }}</span>
      </button>
    </div>
  `,
  styles: [`
    .compare { display: grid; gap: 8px; }
    .suggestion { width: 100%; min-height: 92px; display: grid; grid-template-columns: 30px 1fr auto; gap: 12px; align-items: start; padding: 13px; border: 1px solid #ccd3ce; border-radius: 3px; background: #fbfcf8; color: #18211f; text-align: left; cursor: pointer; }
    .suggestion:hover:not(:disabled) { border-color: #6e8c83; background: #f1f6f2; }
    .suggestion.selected { border-color: #1c625b; box-shadow: inset 3px 0 #1c625b; }
    .suggestion:disabled { cursor: default; opacity: 1; }
    .rank { display: grid; place-items: center; width: 26px; height: 26px; background: #e6ebe7; color: #54615d; font-size: 11px; font-weight: 800; }
    .body strong, .body small { display: block; }
    .body strong { font-size: 13px; }
    .body small { margin-top: 5px; color: #66716d; font-size: 11px; line-height: 1.45; }
    .shift { display: grid; gap: 4px; margin: 8px 0 2px; padding: 8px 9px; border-left: 3px solid #1c625b; background: #e9efec; }
    .shift b { font-size: 11px; text-transform: uppercase; letter-spacing: .04em; }
    .shift .utc { display: grid; grid-template-columns: 46px 1fr; gap: 8px; font-size: 10px; font-variant-numeric: tabular-nums; }
    .shift .utc i { font-style: normal; text-transform: uppercase; color: #66716d; }
    .shift .utc.old { color: #8a4b35; }
    .shift .utc.new { color: #1c625b; font-weight: 700; }
    .tags { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 9px; }
    .tags i { padding: 2px 6px; background: #e9eeea; font-size: 9px; font-style: normal; text-transform: uppercase; }
    .choice { align-self: center; color: #1c625b; font-size: 9px; font-weight: 800; }
    @media (max-width: 560px) { .suggestion { grid-template-columns: 26px 1fr; } .choice { display: none; } }
  `],
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResolutionComparePanelComponent {
  @Input({ required: true }) resolution: ConflictResolution | null = null;
  @Input() selectedKey = '';
  @Input() readonly = false;
  @Output() selectedKeyChange = new EventEmitter<string>();
  readonly microShiftType = MICRO_TIME_SHIFT;
  choose(suggestion: ResolutionSuggestion): void { if (!this.readonly) this.selectedKeyChange.emit(suggestion.action_key); }
  shiftLabel(minutes: number): string { return minutes === 0 ? 'no time shift' : Math.abs(minutes) + (minutes < 0 ? ' min earlier' : ' min later'); }
  interval(start?: string, end?: string): string { return start && end ? `${formatUtc(start)} – ${formatUtc(end)}` : '—'; }
}
