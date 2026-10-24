#include "bridge.h"
#include <SDL3_ttf/SDL_ttf.h>
#include <math.h>
#include <string.h>

static SDL_Window *window;
static SDL_Renderer *renderer;
static TTF_Font *font;
static float scale=1.0f, override_scale=0.0f, font_size=0;

static void metrics(void) {
 if(!window || !renderer) return;
 scale=override_scale>0?override_scale:SDL_GetWindowDisplayScale(window);
 if(scale<0.75f)scale=0.75f;if(scale>4.0f)scale=4.0f;
 SDL_SetRenderScale(renderer,scale,scale);
}
int wolf_init(const void *data,int length) {
 if (!SDL_Init(SDL_INIT_VIDEO) || !TTF_Init()) return 0;
 SDL_DisplayID display=SDL_GetPrimaryDisplay();
 SDL_Rect usable={0,0,1280,800};SDL_GetDisplayUsableBounds(display,&usable);
 float content=SDL_GetDisplayContentScale(display);if(content<=0)content=1;
 int w=SDL_min((int)(usable.w*0.84f),(int)(1180*content));
 int h=SDL_min((int)(usable.h*0.84f),(int)(760*content));
 if (!SDL_CreateWindowAndRenderer("FtR WolfTunnel",w,h,SDL_WINDOW_RESIZABLE|SDL_WINDOW_HIGH_PIXEL_DENSITY,&window,&renderer)) return 0;
 SDL_SetWindowMinimumSize(window,SDL_min(w,(int)(520*content)),SDL_min(h,(int)(420*content)));
 SDL_SetWindowPosition(window,SDL_WINDOWPOS_CENTERED,SDL_WINDOWPOS_CENTERED);
 SDL_SetRenderLogicalPresentation(renderer,0,0,SDL_LOGICAL_PRESENTATION_DISABLED);
 font=TTF_OpenFontIO(SDL_IOFromConstMem(data,length),true,16);
 if (!font) return 0;
 override_scale=0;font_size=0;metrics();SDL_StartTextInput(window);return 1;
}
void wolf_destroy(void) {
 if(font) TTF_CloseFont(font);if(renderer) SDL_DestroyRenderer(renderer);if(window) SDL_DestroyWindow(window);
 font=NULL;renderer=NULL;window=NULL;TTF_Quit();SDL_Quit();
}
void wolf_dimensions(int *w,int *h,int *pw,int *ph,float *dpi) {
 metrics();SDL_GetRenderOutputSize(renderer,pw,ph);*w=(int)(*pw/scale);*h=(int)(*ph/scale);*dpi=scale;
}
void wolf_resize(int w,int h) {SDL_SetWindowSize(window,w,h);metrics();}
void wolf_scale(float value) {override_scale=value;metrics();}
int wolf_poll(WolfEvent *out) {
 static SDL_Event event;if(!SDL_PollEvent(&event)) return 0;
 memset(out,0,sizeof(*out));metrics();SDL_ConvertEventToRenderCoordinates(renderer,&event);
 if(event.type==SDL_EVENT_QUIT)out->type=1;
 if(event.type==SDL_EVENT_MOUSE_BUTTON_DOWN && event.button.button==SDL_BUTTON_LEFT){out->type=2;out->x=event.button.x;out->y=event.button.y;}
 if(event.type==SDL_EVENT_TEXT_INPUT){out->type=3;out->text=event.text.text;}
 if(event.type==SDL_EVENT_KEY_DOWN){out->type=4;out->key=(int)event.key.key;out->ctrl=(event.key.mod&(SDL_KMOD_CTRL|SDL_KMOD_GUI))!=0;out->shift=(event.key.mod&SDL_KMOD_SHIFT)!=0;}
 if(event.type==SDL_EVENT_WINDOW_EXPOSED || event.type==SDL_EVENT_WINDOW_RESIZED || event.type==SDL_EVENT_WINDOW_PIXEL_SIZE_CHANGED || event.type==SDL_EVENT_WINDOW_DISPLAY_SCALE_CHANGED)out->type=5;
 if(event.type==SDL_EVENT_MOUSE_WHEEL){out->type=6;out->y=event.wheel.y;}
 return 1;
}
static void color(int c){SDL_SetRenderDrawColor(renderer,(c>>16)&255,(c>>8)&255,c&255,255);}
void wolf_clear(void){metrics();color(0x10161f);SDL_RenderClear(renderer);}
void wolf_rect(float x,float y,float w,float h,int c){color(c);SDL_FRect r={x,y,w,h};SDL_RenderFillRect(renderer,&r);}
void wolf_clip(int x,int y,int w,int h){if(w<=0){SDL_SetRenderClipRect(renderer,NULL);return;}SDL_Rect r={x,y,w,h};SDL_SetRenderClipRect(renderer,&r);}
static void size(float points){float actual=points*scale;if(actual!=font_size){TTF_SetFontSize(font,actual);font_size=actual;}}
void wolf_text_sized(float x,float y,float w,const char *text,int c,float points){
 if(!text || !*text || w<=0)return;size(points);
 SDL_Color fg={(c>>16)&255,(c>>8)&255,c&255,255};SDL_Surface *surface=TTF_RenderText_Blended(font,text,0,fg);if(!surface)return;
 SDL_Texture *texture=SDL_CreateTextureFromSurface(renderer,surface);float width=(float)surface->w,height=(float)surface->h;SDL_DestroySurface(surface);
 if(texture){
  // Rasterise at physical DPI, then map each texture pixel to one output pixel.
  SDL_SetTextureScaleMode(texture,SDL_SCALEMODE_NEAREST);
  SDL_FRect src={0,0,SDL_min(width,floorf(w*scale)),height};
  SDL_FRect dest={roundf(x*scale)/scale,roundf(y*scale)/scale,src.w/scale,height/scale};
  SDL_RenderTexture(renderer,texture,&src,&dest);SDL_DestroyTexture(texture);
 }
}
void wolf_text(float x,float y,float w,const char *text,int c){wolf_text_sized(x,y,w,text,c,16);}
float wolf_text_width(const char *text){size(16);int w=0,h=0;TTF_GetStringSize(font,text,0,&w,&h);return w/scale;}
void wolf_present(void){SDL_RenderPresent(renderer);}
int wolf_capture(const char *path){SDL_Surface *s=SDL_RenderReadPixels(renderer,NULL);if(!s)return 0;int ok=SDL_SaveBMP(s,path);SDL_DestroySurface(s);return ok;}
void wolf_click(float x,float y){SDL_Event e;SDL_zero(e);e.type=SDL_EVENT_MOUSE_BUTTON_DOWN;e.button.windowID=SDL_GetWindowID(window);e.button.button=SDL_BUTTON_LEFT;SDL_RenderCoordinatesToWindow(renderer,x,y,&e.button.x,&e.button.y);SDL_PushEvent(&e);}
void wolf_input(const char *text){static char injected[8193];SDL_strlcpy(injected,text,sizeof(injected));SDL_Event e;SDL_zero(e);e.type=SDL_EVENT_TEXT_INPUT;e.text.text=injected;SDL_PushEvent(&e);}
void wolf_key(int key,int ctrl){SDL_Event e;SDL_zero(e);e.type=SDL_EVENT_KEY_DOWN;e.key.key=key;e.key.mod=ctrl?SDL_KMOD_CTRL:0;SDL_PushEvent(&e);}
char *wolf_clipboard(void){return SDL_GetClipboardText();}
