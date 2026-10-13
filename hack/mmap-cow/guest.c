// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Synthetic initramfs PID 1 for the bounded CH CoW test; no disk or network.
#include <stdio.h>
#include <termios.h>
#include <unistd.h>

static volatile unsigned char state[16 * 1024 * 1024];
static volatile unsigned char mode = 'I';

int main(void) {
    struct termios term;
    if (tcgetattr(0, &term) == 0) {
        cfmakeraw(&term);
        tcsetattr(0, TCSANOW, &term);
    }
    setvbuf(stdout, NULL, _IONBF, 0);
    state[0] = 0x31;
    puts("COW-READY");
    for (;;) {
        unsigned char command;
        if (read(0, &command, 1) != 1) continue;
        if (command == 'A') { state[0] = 0xa1; state[sizeof(state)-1] = 0xa2; mode = 'A'; }
        if (command == 'B') { state[0] = 0xb1; state[sizeof(state)-1] = 0xb2; mode = 'B'; }
        if (command == 'D') { for (size_t i = 0; i < sizeof(state); i++) state[i] = 0xdd; mode = 'D'; }
        if (mode != 'I' && mode != 'A' && mode != 'B' && mode != 'D') {
            printf("COW-ERROR:invalid-mode=%02x\n", mode);
            continue;
        }
        unsigned char first = mode == 'A' ? 0xa1 : mode == 'B' ? 0xb1 : mode == 'D' ? 0xdd : 0x31;
        unsigned char last = mode == 'A' ? 0xa2 : mode == 'B' ? 0xb2 : mode == 'D' ? 0xdd : 0;
        size_t verified = 0;
        for (; verified < sizeof(state); verified++) {
            unsigned char expected = verified == 0 ? first : verified == sizeof(state)-1 ? last : mode == 'D' ? 0xdd : 0;
            unsigned char actual = state[verified];
            if (actual != expected) {
                printf("COW-ERROR:offset=%zu actual=%02x expected=%02x\n", verified, actual, expected);
                break;
            }
        }
        if (verified == sizeof(state)) {
            printf("COW:%02x:%02x:verified=%zu\n", first, last, verified);
        }
    }
}
