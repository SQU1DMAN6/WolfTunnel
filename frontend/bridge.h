#ifndef WOLF_BRIDGE_H
#define WOLF_BRIDGE_H
#include <SDL3/SDL.h>
#include <stdlib.h>
typedef struct { int type,key,ctrl,shift; float x,y; const char *text; } WolfEvent;
int wolf_init(const void *font, int length);
void wolf_destroy(void);
void wolf_dimensions(int *w,int *h,int *pw,int *ph,float *scale);
void wolf_resize(int w,int h);
void wolf_scale(float scale);
void wolf_clip(int x,int y,int w,int h);
void wolf_text_sized(float x,float y,float w,const char *text,int color,float size);
float wolf_text_width(const char *text);
int wolf_poll(WolfEvent *out);
void wolf_clear(void);
void wolf_rect(float x,float y,float w,float h,int color);
void wolf_text(float x,float y,float w,const char *text,int color);
void wolf_present(void);
int wolf_capture(const char *path);
void wolf_click(float x,float y);
void wolf_input(const char *text);
void wolf_key(int key,int ctrl);
char *wolf_clipboard(void);
#endif
