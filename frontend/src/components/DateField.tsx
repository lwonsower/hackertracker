import { DatePicker, parseDate, Portal } from '@chakra-ui/react'
import type { ReactNode } from 'react'

// Chakra ships no calendar glyph and the trigger renders whatever it is given.
function CalendarIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="1em"
      height="1em"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      aria-hidden="true"
    >
      <rect x="3" y="5" width="18" height="16" rx="2" />
      <path d="M3 10h18M8 3v4M16 3v4" />
    </svg>
  )
}

/** A date picker over a `YYYY-MM-DD` string, which is what the API takes. */
export default function DateField({
  label,
  value,
  onChange,
  flex,
}: {
  label: ReactNode
  value: string
  onChange: (value: string) => void
  flex?: string
}) {
  return (
    <DatePicker.Root
      value={value ? [parseDate(value)] : []}
      onValueChange={(details) => onChange(details.valueAsString[0] ?? '')}
      flex={flex}
      // Without this the input's intrinsic width wins and the field overflows
      // whatever column it sits in.
      minW="0"
    >
      <DatePicker.Label>{label}</DatePicker.Label>
      <DatePicker.Control>
        <DatePicker.Input index={0} />
        <DatePicker.IndicatorGroup>
          <DatePicker.Trigger>
            <CalendarIcon />
          </DatePicker.Trigger>
        </DatePicker.IndicatorGroup>
      </DatePicker.Control>
      <Portal>
        <DatePicker.Positioner>
          <DatePicker.Content>
            {/* Header and DayTable are Chakra's assembled parts. */}
            <DatePicker.View view="day">
              <DatePicker.Header />
              <DatePicker.DayTable />
            </DatePicker.View>
            <DatePicker.View view="month">
              <DatePicker.Header />
              <DatePicker.MonthTable />
            </DatePicker.View>
            <DatePicker.View view="year">
              <DatePicker.Header />
              <DatePicker.YearTable />
            </DatePicker.View>
          </DatePicker.Content>
        </DatePicker.Positioner>
      </Portal>
    </DatePicker.Root>
  )
}
