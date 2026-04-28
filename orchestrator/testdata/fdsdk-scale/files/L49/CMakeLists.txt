cmake_minimum_required(VERSION 3.20)
project(L49 LANGUAGES C VERSION 0.1.0)

find_package(L34 CONFIG REQUIRED)
find_package(L39 CONFIG REQUIRED)
find_package(L41 CONFIG REQUIRED)

add_library(L49 STATIC src/L49.c)
target_include_directories(L49 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L49 PUBLIC L34::L34 L39::L39 L41::L41)

install(TARGETS L49
        EXPORT L49Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L49Targets
        FILE L49Targets.cmake
        NAMESPACE L49::
        DESTINATION lib/cmake/L49)
